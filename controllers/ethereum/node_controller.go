package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ethereumv1alpha1 "github.com/kotalco/kotal/apis/ethereum/v1alpha1"
	"github.com/kotalco/kotal/helpers"
)

// NodeReconciler reconciles a Node object
type NodeReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ethereum.kotal.io,resources=nodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ethereum.kotal.io,resources=nodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=watch;get;list;create;update;delete
// +kubebuilder:rbac:groups=core,resources=secrets;services;configmaps;persistentvolumeclaims,verbs=watch;get;create;update;list;delete

// Reconcile reconciles ethereum networks
func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {

	var node ethereumv1alpha1.Node

	if err = r.Client.Get(context.Background(), req.NamespacedName, &node); err != nil {
		err = client.IgnoreNotFound(err)
		return
	}

	r.updateLabels(&node)
	r.updateStaticNodes(&node)

	if err = r.reconcileNodeDataPVC(&node); err != nil {
		return
	}

	if err = r.reconcileNodeConfigmap(&node); err != nil {
		return
	}

	ip, err := r.reconcileNodeService(&node)
	if err != nil {
		return
	}

	if err = r.reconcileNodeStatefulSet(&node); err != nil {
		return
	}

	if node.Spec.Nodekey == "" && node.Spec.Import == nil {
		return
	}

	var publicKey string
	if publicKey, err = r.reconcileNodeSecret(&node); err != nil {
		return
	}

	if node.Spec.Bootnode != true {
		return
	}

	enodeURL := fmt.Sprintf("enode://%s@%s:%d", publicKey, ip, node.Spec.P2PPort)

	if err = r.updateStatus(&node, enodeURL); err != nil {
		return
	}

	return ctrl.Result{}, nil
}

// updateLabels adds missing labels to the node
func (r *NodeReconciler) updateLabels(node *ethereumv1alpha1.Node) {

	if node.Labels == nil {
		node.Labels = map[string]string{}
	}

	node.Labels["name"] = "node"
	node.Labels["protocol"] = "ethereum"
	node.Labels["client"] = string(node.Spec.Client)

	if node.Labels["instance"] == "" {
		node.Labels["instance"] = node.Name
	}
}

// updateStatus updates network status
func (r *NodeReconciler) updateStatus(node *ethereumv1alpha1.Node, enodeURL string) error {
	node.Status.EnodeURL = enodeURL

	if err := r.Status().Update(context.Background(), node); err != nil {
		r.Log.Error(err, "unable to update node status")
		return err
	}

	return nil
}

// updateStaticNodes updates node static nodes with passed static nodes through annotations
// when node is part of network, static nodes are passed through annotations
func (r *NodeReconciler) updateStaticNodes(node *ethereumv1alpha1.Node) {
	if node.Annotations[staticNodesAnnotation] == "" {
		return
	}
	staticNodes := strings.Split(node.Annotations[staticNodesAnnotation], ";")

	for i := range staticNodes {
		node.Spec.StaticNodes = append(node.Spec.StaticNodes, ethereumv1alpha1.Enode(staticNodes[i]))
	}
}

// specNodeConfigmap updates genesis configmap spec
func (r *NodeReconciler) specNodeConfigmap(client ethereumv1alpha1.EthereumClient, configmap *corev1.ConfigMap, genesis, initGenesisScript, importAccountScript, staticNodes string) {
	if configmap.Data == nil {
		configmap.Data = map[string]string{}
	}

	configmap.Data["genesis.json"] = genesis
	configmap.Data["init-genesis.sh"] = initGenesisScript
	configmap.Data["import-account.sh"] = importAccountScript

	var key string

	switch client {
	case ethereumv1alpha1.GethClient:
		key = "config.toml"
	case ethereumv1alpha1.BesuClient:
		key = "static-nodes.json"
	case ethereumv1alpha1.ParityClient:
		key = "static-nodes"
	}

	currentStaticNodes := configmap.Data[key]
	// update static nodes config if it's empty
	// update static nodes config if more static nodes has been created
	if currentStaticNodes == "" || len(currentStaticNodes) < len(staticNodes) {
		configmap.Data[key] = staticNodes
	}
}

// reconcileNodeConfigmap creates genesis config map if it doesn't exist or update it
func (r *NodeReconciler) reconcileNodeConfigmap(node *ethereumv1alpha1.Node) error {

	var genesis, initGenesisScript, importAccountScript string

	configmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name,
			Namespace: node.Namespace,
		},
	}

	client, err := NewEthereumClient(node.Spec.Client)
	if err != nil {
		return err
	}

	staticNodes := client.EncodeStaticNodes(node)

	// private network with custom genesis
	if node.Spec.Genesis != nil {

		// create client specific genesis configuration
		if genesis, err = client.GetGenesisFile(node); err != nil {
			return err
		}
		// create init genesis script if client is geth
		if node.Spec.Client == ethereumv1alpha1.GethClient {
			initGenesisScript, err = generateInitGenesisScript()
			if err != nil {
				return err
			}
		}
	}

	// geth and parity
	// create import account script
	if node.Spec.Import != nil {
		var err error
		importAccountScript, err = generateImportAccountScript(node.Spec.Client)
		if err != nil {
			return err
		}
	}

	_, err = ctrl.CreateOrUpdate(context.Background(), r.Client, configmap, func() error {
		if err := ctrl.SetControllerReference(node, configmap, r.Scheme); err != nil {
			r.Log.Error(err, "Unable to set controller reference on genesis configmap")
			return err
		}

		r.specNodeConfigmap(node.Spec.Client, configmap, genesis, initGenesisScript, importAccountScript, staticNodes)

		return nil
	})

	return err
}

// specNodeDataPVC update node data pvc spec
func (r *NodeReconciler) specNodeDataPVC(pvc *corev1.PersistentVolumeClaim, node *ethereumv1alpha1.Node) {
	request := corev1.ResourceList{
		corev1.ResourceStorage: resource.MustParse(node.Spec.Resources.Storage),
	}

	// spec is immutable after creation except resources.requests for bound claims
	if !pvc.CreationTimestamp.IsZero() {
		pvc.Spec.Resources.Requests = request
		return
	}

	pvc.ObjectMeta.Labels = node.GetLabels()
	pvc.Spec = corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{
			corev1.ReadWriteOnce,
		},
		Resources: corev1.ResourceRequirements{
			Requests: request,
		},
		StorageClassName: node.Spec.Resources.StorageClass,
	}
}

// reconcileNodeDataPVC creates node data pvc if it doesn't exist
func (r *NodeReconciler) reconcileNodeDataPVC(node *ethereumv1alpha1.Node) error {

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name,
			Namespace: node.Namespace,
		},
	}

	_, err := ctrl.CreateOrUpdate(context.Background(), r.Client, pvc, func() error {
		if err := ctrl.SetControllerReference(node, pvc, r.Scheme); err != nil {
			return err
		}
		r.specNodeDataPVC(pvc, node)
		return nil
	})

	return err
}

// createNodeVolumes creates all the required volumes for the node
func (r *NodeReconciler) createNodeVolumes(node *ethereumv1alpha1.Node) []corev1.Volume {

	volumes := []corev1.Volume{}

	if node.Spec.Nodekey != "" || node.Spec.Import != nil {
		secretsVolume := corev1.Volume{
			Name: "secrets",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: node.Name,
				},
			},
		}
		volumes = append(volumes, secretsVolume)
	}

	configVolume := corev1.Volume{
		Name: "config",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: node.Name,
				},
			},
		},
	}
	volumes = append(volumes, configVolume)

	dataVolume := corev1.Volume{
		Name: "data",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: node.Name,
			},
		},
	}
	volumes = append(volumes, dataVolume)

	return volumes
}

// createNodeVolumeMounts creates all required volume mounts for the node
func (r *NodeReconciler) createNodeVolumeMounts(node *ethereumv1alpha1.Node) []corev1.VolumeMount {

	volumeMounts := []corev1.VolumeMount{}

	if node.Spec.Nodekey != "" || node.Spec.Import != nil {
		nodekeyMount := corev1.VolumeMount{
			Name:      "secrets",
			MountPath: PathSecrets,
			ReadOnly:  true,
		}
		volumeMounts = append(volumeMounts, nodekeyMount)
	}

	genesisMount := corev1.VolumeMount{
		Name:      "config",
		MountPath: PathConfig,
		ReadOnly:  true,
	}
	volumeMounts = append(volumeMounts, genesisMount)

	dataMount := corev1.VolumeMount{
		Name:      "data",
		MountPath: PathBlockchainData,
	}
	volumeMounts = append(volumeMounts, dataMount)

	return volumeMounts
}

// getNodeAffinity returns affinity settings to be use by the node pod
func (r *NodeReconciler) getNodeAffinity(node *ethereumv1alpha1.Node) *corev1.Affinity {
	if node.Spec.HighlyAvailable {
		return &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"name":    "node",
								"network": node.Name,
							},
						},
						TopologyKey: node.Spec.TopologyKey,
					},
				},
			},
		}
	}
	return nil
}

// specNodeStatefulSet updates node statefulset spec
func (r *NodeReconciler) specNodeStatefulSet(sts *appsv1.StatefulSet, node *ethereumv1alpha1.Node, args []string, volumes []corev1.Volume, volumeMounts []corev1.VolumeMount, affinity *corev1.Affinity) {
	labels := node.GetLabels()
	// used by geth to init genesis and import account(s)
	initContainers := []corev1.Container{}
	// node client container
	nodeContainer := corev1.Container{
		Name: "node",
		Args: args,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(node.Spec.Resources.CPU),
				corev1.ResourceMemory: resource.MustParse(node.Spec.Resources.Memory),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(node.Spec.Resources.CPULimit),
				corev1.ResourceMemory: resource.MustParse(node.Spec.Resources.MemoryLimit),
			},
		},
		VolumeMounts: volumeMounts,
	}

	// besu starts non root user
	// digital ocean doesn't support kubernetes securityContext:{runAsUser, fsGroup}
	dataDirPermissionFix := corev1.Container{
		Name:         "data-dir-permission-fix",
		Image:        "busybox",
		Command:      []string{"/bin/chmod"},
		Args:         []string{"-R", "777", PathBlockchainData},
		VolumeMounts: volumeMounts,
	}

	if node.Spec.Client == ethereumv1alpha1.GethClient {
		if node.Spec.Genesis != nil {
			initGenesis := corev1.Container{
				Name:         "init-genesis",
				Image:        GethImage(),
				Command:      []string{"/bin/sh"},
				Args:         []string{fmt.Sprintf("%s/init-genesis.sh", PathConfig)},
				VolumeMounts: volumeMounts,
			}
			initContainers = append(initContainers, initGenesis)
		}
		if node.Spec.Import != nil {
			importAccount := corev1.Container{
				Name:         "import-account",
				Image:        GethImage(),
				Command:      []string{"/bin/sh"},
				Args:         []string{fmt.Sprintf("%s/import-account.sh", PathConfig)},
				VolumeMounts: volumeMounts,
			}
			initContainers = append(initContainers, importAccount)
		}

		nodeContainer.Image = GethImage()
	} else if node.Spec.Client == ethereumv1alpha1.BesuClient {
		linkStaticNodes := corev1.Container{
			Name:    "link-static-nodes",
			Image:   "busybox",
			Command: []string{"/bin/ln"},
			Args: []string{
				"-sfn",
				fmt.Sprintf("%s/static-nodes.json", PathConfig),
				fmt.Sprintf("%s/static-nodes.json", PathBlockchainData),
			},
			VolumeMounts: volumeMounts,
		}
		initContainers = append(initContainers, linkStaticNodes)
		initContainers = append(initContainers, dataDirPermissionFix)
		nodeContainer.Image = BesuImage()
	} else if node.Spec.Client == ethereumv1alpha1.ParityClient {
		initContainers = append(initContainers, dataDirPermissionFix)
		if node.Spec.Import != nil {
			importAccount := corev1.Container{
				Name:         "import-account",
				Image:        ParityImage(),
				Command:      []string{"/bin/sh"},
				Args:         []string{fmt.Sprintf("%s/import-account.sh", PathConfig)},
				VolumeMounts: volumeMounts,
			}
			initContainers = append(initContainers, importAccount)
		}
		nodeContainer.Image = ParityImage()
	}

	sts.ObjectMeta.Labels = labels
	if sts.Spec.Selector == nil {
		sts.Spec.Selector = &metav1.LabelSelector{}
	}
	sts.Spec.ServiceName = node.Name
	sts.Spec.Selector.MatchLabels = labels
	sts.Spec.Template.ObjectMeta.Labels = labels
	sts.Spec.Template.Spec = corev1.PodSpec{
		Volumes:        volumes,
		InitContainers: initContainers,
		Containers:     []corev1.Container{nodeContainer},
		Affinity:       affinity,
	}
}

// reconcileNodeStatefulSet creates node statefulset if it doesn't exist, update it if it does exist
func (r *NodeReconciler) reconcileNodeStatefulSet(node *ethereumv1alpha1.Node) error {

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name,
			Namespace: node.Namespace,
		},
	}

	client, err := NewEthereumClient(node.Spec.Client)
	if err != nil {
		return err
	}
	args := client.GetArgs(node)
	volumes := r.createNodeVolumes(node)
	mounts := r.createNodeVolumeMounts(node)
	affinity := r.getNodeAffinity(node)

	_, err = ctrl.CreateOrUpdate(context.Background(), r.Client, sts, func() error {
		if err := ctrl.SetControllerReference(node, sts, r.Scheme); err != nil {
			return err
		}
		r.specNodeStatefulSet(sts, node, args, volumes, mounts, affinity)
		return nil
	})

	return err
}

func (r *NodeReconciler) specNodeSecret(secret *corev1.Secret, node *ethereumv1alpha1.Node) error {
	secret.ObjectMeta.Labels = node.GetLabels()
	data := map[string]string{}

	if node.Spec.Nodekey != "" {
		data["nodekey"] = string(node.Spec.Nodekey)[2:]
	}

	if node.Spec.Import != nil {
		if node.Spec.Client == ethereumv1alpha1.ParityClient {
			account, err := KeyStoreFromPrivatekey(string(node.Spec.Import.PrivateKey)[2:], node.Spec.Import.Password)
			if err != nil {
				return err
			}
			secret.Data = map[string][]byte{
				"account": account,
			}
		}

		data["account.key"] = string(node.Spec.Import.PrivateKey)[2:]
		data["account.password"] = node.Spec.Import.Password
	}

	secret.StringData = data

	return nil
}

// reconcileNodeSecret creates node secret if it doesn't exist, update it if it exists
func (r *NodeReconciler) reconcileNodeSecret(node *ethereumv1alpha1.Node) (publicKey string, err error) {

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name,
			Namespace: node.Namespace,
		},
	}

	if node.Spec.Nodekey != "" {
		// hex private key without the leading 0x
		privateKey := string(node.Spec.Nodekey)[2:]
		publicKey, err = helpers.DerivePublicKey(privateKey)
		if err != nil {
			return
		}
	}

	_, err = ctrl.CreateOrUpdate(context.Background(), r.Client, secret, func() error {
		if err := ctrl.SetControllerReference(node, secret, r.Scheme); err != nil {
			return err
		}

		return r.specNodeSecret(secret, node)
	})

	return
}

// specNodeService updates node service spec
func (r *NodeReconciler) specNodeService(svc *corev1.Service, node *ethereumv1alpha1.Node) {
	labels := node.GetLabels()
	client := node.Spec.Client

	svc.ObjectMeta.Labels = labels
	svc.Spec.Ports = []corev1.ServicePort{
		{
			Name:       "discovery",
			Port:       int32(node.Spec.P2PPort),
			TargetPort: intstr.FromInt(int(node.Spec.P2PPort)),
			Protocol:   corev1.ProtocolUDP,
		},
		{
			Name:       "p2p",
			Port:       int32(node.Spec.P2PPort),
			TargetPort: intstr.FromInt(int(node.Spec.P2PPort)),
			Protocol:   corev1.ProtocolTCP,
		},
	}

	if node.Spec.RPCPort != 0 {
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{
			Name:       "json-rpc",
			Port:       int32(node.Spec.RPCPort),
			TargetPort: intstr.FromInt(int(node.Spec.RPCPort)),
			Protocol:   corev1.ProtocolTCP,
		})
	}

	if node.Spec.WSPort != 0 {
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{
			Name:       "ws",
			Port:       int32(node.Spec.WSPort),
			TargetPort: intstr.FromInt(int(node.Spec.WSPort)),
			Protocol:   corev1.ProtocolTCP,
		})
	}

	if node.Spec.GraphQLPort != 0 {
		targetPort := node.Spec.GraphQLPort
		if client == ethereumv1alpha1.GethClient {
			targetPort = node.Spec.RPCPort
		}
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{
			Name:       "graphql",
			Port:       int32(node.Spec.GraphQLPort),
			TargetPort: intstr.FromInt(int(targetPort)),
			Protocol:   corev1.ProtocolTCP,
		})
	}

	svc.Spec.Selector = labels
}

// reconcileNodeService reconciles node service
func (r *NodeReconciler) reconcileNodeService(node *ethereumv1alpha1.Node) (ip string, err error) {

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.Name,
			Namespace: node.Namespace,
		},
	}

	_, err = ctrl.CreateOrUpdate(context.Background(), r.Client, svc, func() error {
		if err = ctrl.SetControllerReference(node, svc, r.Scheme); err != nil {
			return err
		}

		r.specNodeService(svc, node)

		return nil
	})

	if err != nil {
		return
	}

	ip = svc.Spec.ClusterIP

	return
}

// SetupWithManager adds reconciler to the manager
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ethereumv1alpha1.Node{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}
