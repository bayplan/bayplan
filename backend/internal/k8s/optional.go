package k8s

import (
	"sync"

	autoscalingv2listers "k8s.io/client-go/listers/autoscaling/v2"
	batchv1listers "k8s.io/client-go/listers/batch/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	networkingv1listers "k8s.io/client-go/listers/networking/v1"
	storagev1listers "k8s.io/client-go/listers/storage/v1"
	"k8s.io/client-go/tools/cache"
)

// Names of the optional informers. They are also the API path segment
// (/api/clusters/{id}/<name>) and the frontend tab.
const (
	KindConfigMaps      = "configmaps"
	KindSecrets         = "secrets"
	KindIngresses       = "ingresses"
	KindNetworkPolicies = "networkpolicies"
	KindPVCs            = "persistentvolumeclaims"
	KindPVs             = "persistentvolumes"
	KindStorageClasses  = "storageclasses"
	KindJobs            = "jobs"
	KindCronJobs        = "cronjobs"
	KindHPAs            = "horizontalpodautoscalers"
	KindServiceAccounts = "serviceaccounts"
	KindResourceQuotas  = "resourcequotas"
	KindLimitRanges     = "limitranges"
)

// OptionalKinds lists the optional kinds, to validate routes and so the UI
// knows which tabs exist.
var OptionalKinds = []string{
	KindConfigMaps, KindSecrets, KindIngresses, KindNetworkPolicies, KindPVCs, KindPVs,
	KindStorageClasses, KindJobs, KindCronJobs, KindHPAs, KindServiceAccounts, KindResourceQuotas, KindLimitRanges,
}

// optionalListers groups the listers of the secondary kinds. Separate from
// clusterConn so adding a kind means touching this file and read_extra.go,
// without brushing against the connection's lifecycle.
type optionalListers struct {
	configMaps      corev1listers.ConfigMapLister
	secrets         corev1listers.SecretLister
	serviceAccounts corev1listers.ServiceAccountLister
	pvcs            corev1listers.PersistentVolumeClaimLister
	pvs             corev1listers.PersistentVolumeLister
	ingresses       networkingv1listers.IngressLister
	networkPolicies networkingv1listers.NetworkPolicyLister
	storageClasses  storagev1listers.StorageClassLister
	jobs            batchv1listers.JobLister
	cronJobs        batchv1listers.CronJobLister
	hpas            autoscalingv2listers.HorizontalPodAutoscalerLister
	resourceQuotas  corev1listers.ResourceQuotaLister
	limitRanges     corev1listers.LimitRangeLister

	// errMu protects optionalErrors: the last list/watch error PER KIND.
	// Deliberately separate from clusterConn.lastError: the ClusterRole missing
	// the ingresses permission cannot paint the whole cluster red when pods and
	// deployments work perfectly.
	errMu  sync.RWMutex
	errors map[string]string
}

// registerOptionalInformers registers the informers of the secondary kinds.
// They are checked individually on read (getOptionalConn): if the cluster lacks
// the permission for one, only that listing responds 503 and the rest goes on.
//
// Secrets are cached whole (values included): the informer cannot filter
// fields. The protection is in the read layer (ListSecrets never returns data;
// the YAML redacts it for whoever is not admin of the cluster).
func (m *Manager) registerOptionalInformers(conn *clusterConn) {
	f := conn.factory
	l := &optionalListers{errors: map[string]string{}}

	cm := f.Core().V1().ConfigMaps()
	sec := f.Core().V1().Secrets()
	sa := f.Core().V1().ServiceAccounts()
	pvc := f.Core().V1().PersistentVolumeClaims()
	pv := f.Core().V1().PersistentVolumes()
	ing := f.Networking().V1().Ingresses()
	np := f.Networking().V1().NetworkPolicies()
	sc := f.Storage().V1().StorageClasses()
	job := f.Batch().V1().Jobs()
	cj := f.Batch().V1().CronJobs()
	hpa := f.Autoscaling().V2().HorizontalPodAutoscalers()
	rq := f.Core().V1().ResourceQuotas()
	lr := f.Core().V1().LimitRanges()

	l.configMaps, l.secrets, l.serviceAccounts = cm.Lister(), sec.Lister(), sa.Lister()
	l.pvcs, l.pvs = pvc.Lister(), pv.Lister()
	l.ingresses, l.networkPolicies = ing.Lister(), np.Lister()
	l.storageClasses = sc.Lister()
	l.jobs, l.cronJobs, l.hpas = job.Lister(), cj.Lister(), hpa.Lister()
	l.resourceQuotas, l.limitRanges = rq.Lister(), lr.Lister()

	conn.optional = map[string]cache.SharedIndexInformer{
		KindConfigMaps:      cm.Informer(),
		KindSecrets:         sec.Informer(),
		KindServiceAccounts: sa.Informer(),
		KindPVCs:            pvc.Informer(),
		KindPVs:             pv.Informer(),
		KindIngresses:       ing.Informer(),
		KindNetworkPolicies: np.Informer(),
		KindStorageClasses:  sc.Informer(),
		KindJobs:            job.Informer(),
		KindCronJobs:        cj.Informer(),
		KindHPAs:            hpa.Informer(),
		KindResourceQuotas:  rq.Informer(),
		KindLimitRanges:     lr.Informer(),
	}
	for name, inf := range conn.optional {
		name := name
		_ = inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
			l.errMu.Lock()
			l.errors[name] = describeAPIError(err)
			l.errMu.Unlock()
		})
	}
	conn.opt = l
}

// optionalError returns the last list/watch error of an optional kind.
func (c *clusterConn) optionalError(name string) string {
	if c.opt == nil {
		return ""
	}
	c.opt.errMu.RLock()
	defer c.opt.errMu.RUnlock()
	return c.opt.errors[name]
}

// OptionalKindStatus says, per optional kind, whether it is available in a
// cluster and if not why. The UI uses it to grey out the missing tabs with the
// reason (usually "re-apply the RBAC") instead of an empty table.
type OptionalKindStatus struct {
	Synced bool   `json:"synced"`
	Error  string `json:"error,omitempty"`
}

func (m *Manager) OptionalKinds(id int64) map[string]OptionalKindStatus {
	conn, err := m.getConn(id)
	if err != nil {
		return nil
	}
	out := make(map[string]OptionalKindStatus, len(conn.optional))
	for name, inf := range conn.optional {
		out[name] = OptionalKindStatus{Synced: inf.HasSynced(), Error: conn.optionalError(name)}
	}
	return out
}
