package k8s

import (
	"fmt"
	"sort"
	"strings"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
)

// Summaries of the optional kinds. Same rule as the rest of types.go: only
// what the table needs, never the whole object (and for Secrets, NEVER the
// values).

type ConfigMapSummary struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Keys      []string  `json:"keys"`
	CreatedAt time.Time `json:"createdAt"`
}

type SecretSummary struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Type      string    `json:"type"`
	Keys      []string  `json:"keys"` // key names only; values never leave the backend this way
	CreatedAt time.Time `json:"createdAt"`
}

type ServiceAccountSummary struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Secrets   int       `json:"secrets"`
	CreatedAt time.Time `json:"createdAt"`
}

type IngressSummary struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Class     string    `json:"class"`
	Hosts     []string  `json:"hosts"`
	Paths     []string  `json:"paths"` // "host/path -> svc:port"
	TLS       bool      `json:"tls"`
	Addresses []string  `json:"addresses"`
	CreatedAt time.Time `json:"createdAt"`
}

type NetworkPolicySummary struct {
	Name        string    `json:"name"`
	Namespace   string    `json:"namespace"`
	PodSelector string    `json:"podSelector"`
	PolicyTypes []string  `json:"policyTypes"`
	CreatedAt   time.Time `json:"createdAt"`
}

type PVCSummary struct {
	Name         string    `json:"name"`
	Namespace    string    `json:"namespace"`
	Status       string    `json:"status"`
	Volume       string    `json:"volume"`
	Capacity     string    `json:"capacity"`
	AccessModes  []string  `json:"accessModes"`
	StorageClass string    `json:"storageClass"`
	CreatedAt    time.Time `json:"createdAt"`
}

type PVSummary struct {
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	Capacity      string    `json:"capacity"`
	AccessModes   []string  `json:"accessModes"`
	ReclaimPolicy string    `json:"reclaimPolicy"`
	StorageClass  string    `json:"storageClass"`
	Claim         string    `json:"claim"` // ns/name
	CreatedAt     time.Time `json:"createdAt"`
}

type StorageClassSummary struct {
	Name              string    `json:"name"`
	Provisioner       string    `json:"provisioner"`
	ReclaimPolicy     string    `json:"reclaimPolicy"`
	VolumeBindingMode string    `json:"volumeBindingMode"`
	IsDefault         bool      `json:"isDefault"`
	CreatedAt         time.Time `json:"createdAt"`
}

type JobSummary struct {
	Name        string     `json:"name"`
	Namespace   string     `json:"namespace"`
	Status      string     `json:"status"` // Running | Complete | Failed | Suspended | Pending
	Succeeded   int32      `json:"succeeded"`
	Failed      int32      `json:"failed"`
	Active      int32      `json:"active"`
	Completions int32      `json:"completions"`
	Duration    string     `json:"duration,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type CronJobSummary struct {
	Name         string     `json:"name"`
	Namespace    string     `json:"namespace"`
	Schedule     string     `json:"schedule"`
	Suspended    bool       `json:"suspended"`
	Active       int        `json:"active"`
	LastSchedule *time.Time `json:"lastSchedule,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

type HPASummary struct {
	Name        string    `json:"name"`
	Namespace   string    `json:"namespace"`
	Target      string    `json:"target"` // Kind/name
	MinReplicas int32     `json:"minReplicas"`
	MaxReplicas int32     `json:"maxReplicas"`
	Current     int32     `json:"current"`
	Desired     int32     `json:"desired"`
	Metrics     []string  `json:"metrics"` // "cpu 63%/80%"
	CreatedAt   time.Time `json:"createdAt"`
}

// QuotaResource is one line of a ResourceQuota: what is requested (hard)
// against what is consumed (used) according to the API server itself, which is
// what does the accounting.
type QuotaResource struct {
	Name string `json:"name"`
	Hard string `json:"hard"`
	Used string `json:"used"`
	// Percent is used/hard in %; -1 if it cannot be computed.
	Percent int `json:"percent"`
}

type ResourceQuotaSummary struct {
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Scopes    []string        `json:"scopes"`
	Resources []QuotaResource `json:"resources"`
	// ManagedByPanel: it was created by the panel's quota form (name
	// bayplan-quota); the UI offers to edit it from the form.
	ManagedByPanel bool      `json:"managedByPanel"`
	CreatedAt      time.Time `json:"createdAt"`
}

// LimitRangeItem is one line of a LimitRange flattened per resource.
type LimitRangeItem struct {
	Type           string `json:"type"` // Container, Pod, PersistentVolumeClaim
	Resource       string `json:"resource"`
	Min            string `json:"min,omitempty"`
	Max            string `json:"max,omitempty"`
	Default        string `json:"default,omitempty"`
	DefaultRequest string `json:"defaultRequest,omitempty"`
}

type LimitRangeSummary struct {
	Name           string           `json:"name"`
	Namespace      string           `json:"namespace"`
	Limits         []LimitRangeItem `json:"limits"`
	ManagedByPanel bool             `json:"managedByPanel"`
	CreatedAt      time.Time        `json:"createdAt"`
}

// Names of the objects the namespace quota form creates.
const (
	QuotaObjectName      = "bayplan-quota"
	LimitRangeObjectName = "bayplan-limits"
)

func (m *Manager) ListResourceQuotas(id int64, namespaces []string) ([]ResourceQuotaSummary, error) {
	conn, err := m.getOptionalConn(id, KindResourceQuotas)
	if err != nil {
		return nil, err
	}
	items, err := listNamespaced(namespaces, func(ns string) ([]*corev1.ResourceQuota, error) {
		return conn.opt.resourceQuotas.ResourceQuotas(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list resourcequotas: %w", err)
	}
	out := make([]ResourceQuotaSummary, 0, len(items))
	for _, q := range items {
		s := ResourceQuotaSummary{Name: q.Name, Namespace: q.Namespace, Scopes: []string{}, Resources: []QuotaResource{}, ManagedByPanel: q.Name == QuotaObjectName, CreatedAt: q.CreationTimestamp.Time}
		for _, sc := range q.Spec.Scopes {
			s.Scopes = append(s.Scopes, string(sc))
		}
		// Status.Hard is what the API server really enforces (it may differ from
		// Spec.Hard until the controller has reconciled it).
		hard := q.Status.Hard
		if len(hard) == 0 {
			hard = q.Spec.Hard
		}
		for _, name := range sortedResourceNames(hard) {
			h := hard[name]
			r := QuotaResource{Name: string(name), Hard: h.String(), Percent: -1}
			if u, ok := q.Status.Used[name]; ok {
				r.Used = u.String()
				if h.MilliValue() > 0 {
					r.Percent = int(u.MilliValue() * 100 / h.MilliValue())
				}
			}
			s.Resources = append(s.Resources, r)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func sortedResourceNames(list corev1.ResourceList) []corev1.ResourceName {
	names := make([]corev1.ResourceName, 0, len(list))
	for n := range list {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

func (m *Manager) ListLimitRanges(id int64, namespaces []string) ([]LimitRangeSummary, error) {
	conn, err := m.getOptionalConn(id, KindLimitRanges)
	if err != nil {
		return nil, err
	}
	items, err := listNamespaced(namespaces, func(ns string) ([]*corev1.LimitRange, error) {
		return conn.opt.limitRanges.LimitRanges(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list limitranges: %w", err)
	}
	out := make([]LimitRangeSummary, 0, len(items))
	for _, lr := range items {
		s := LimitRangeSummary{Name: lr.Name, Namespace: lr.Namespace, Limits: []LimitRangeItem{}, ManagedByPanel: lr.Name == LimitRangeObjectName, CreatedAt: lr.CreationTimestamp.Time}
		for _, item := range lr.Spec.Limits {
			names := map[corev1.ResourceName]bool{}
			for _, list := range []corev1.ResourceList{item.Min, item.Max, item.Default, item.DefaultRequest} {
				for n := range list {
					names[n] = true
				}
			}
			for _, n := range sortedResourceNames(toList(names)) {
				row := LimitRangeItem{Type: string(item.Type), Resource: string(n)}
				if q, ok := item.Min[n]; ok {
					row.Min = q.String()
				}
				if q, ok := item.Max[n]; ok {
					row.Max = q.String()
				}
				if q, ok := item.Default[n]; ok {
					row.Default = q.String()
				}
				if q, ok := item.DefaultRequest[n]; ok {
					row.DefaultRequest = q.String()
				}
				s.Limits = append(s.Limits, row)
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func toList(names map[corev1.ResourceName]bool) corev1.ResourceList {
	out := corev1.ResourceList{}
	for n := range names {
		out[n] = resource.Quantity{}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (m *Manager) ListConfigMaps(id int64, namespaces []string) ([]ConfigMapSummary, error) {
	conn, err := m.getOptionalConn(id, KindConfigMaps)
	if err != nil {
		return nil, err
	}
	items, err := listNamespaced(namespaces, func(ns string) ([]*corev1.ConfigMap, error) {
		return conn.opt.configMaps.ConfigMaps(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list configmaps: %w", err)
	}
	out := make([]ConfigMapSummary, 0, len(items))
	for _, c := range items {
		keys := sortedKeys(c.Data)
		keys = append(keys, sortedKeys(c.BinaryData)...)
		out = append(out, ConfigMapSummary{Name: c.Name, Namespace: c.Namespace, Keys: keys, CreatedAt: c.CreationTimestamp.Time})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListSecrets returns metadata: type and key names. Values NEVER leave through
// here; the YAML (ResourceYAML) redacts them except for cluster admins.
func (m *Manager) ListSecrets(id int64, namespaces []string) ([]SecretSummary, error) {
	conn, err := m.getOptionalConn(id, KindSecrets)
	if err != nil {
		return nil, err
	}
	items, err := listNamespaced(namespaces, func(ns string) ([]*corev1.Secret, error) {
		return conn.opt.secrets.Secrets(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list secrets: %w", err)
	}
	out := make([]SecretSummary, 0, len(items))
	for _, s := range items {
		out = append(out, SecretSummary{Name: s.Name, Namespace: s.Namespace, Type: string(s.Type), Keys: sortedKeys(s.Data), CreatedAt: s.CreationTimestamp.Time})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Manager) ListServiceAccounts(id int64, namespaces []string) ([]ServiceAccountSummary, error) {
	conn, err := m.getOptionalConn(id, KindServiceAccounts)
	if err != nil {
		return nil, err
	}
	items, err := listNamespaced(namespaces, func(ns string) ([]*corev1.ServiceAccount, error) {
		return conn.opt.serviceAccounts.ServiceAccounts(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list serviceaccounts: %w", err)
	}
	out := make([]ServiceAccountSummary, 0, len(items))
	for _, s := range items {
		out = append(out, ServiceAccountSummary{Name: s.Name, Namespace: s.Namespace, Secrets: len(s.Secrets), CreatedAt: s.CreationTimestamp.Time})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Manager) ListIngresses(id int64, namespaces []string) ([]IngressSummary, error) {
	conn, err := m.getOptionalConn(id, KindIngresses)
	if err != nil {
		return nil, err
	}
	items, err := listNamespaced(namespaces, func(ns string) ([]*networkingv1.Ingress, error) {
		return conn.opt.ingresses.Ingresses(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list ingresses: %w", err)
	}
	out := make([]IngressSummary, 0, len(items))
	for _, ing := range items {
		// Empty lists, never nil: the JSON would be null and the table joins
		// them (an Ingress without an address left the whole tab blank).
		s := IngressSummary{Name: ing.Name, Namespace: ing.Namespace, TLS: len(ing.Spec.TLS) > 0, CreatedAt: ing.CreationTimestamp.Time,
			Hosts: []string{}, Paths: []string{}, Addresses: []string{}}
		if ing.Spec.IngressClassName != nil {
			s.Class = *ing.Spec.IngressClassName
		} else if v := ing.Annotations["kubernetes.io/ingress.class"]; v != "" {
			s.Class = v
		}
		for _, rule := range ing.Spec.Rules {
			host := rule.Host
			if host == "" {
				host = "*"
			}
			s.Hosts = append(s.Hosts, host)
			if rule.HTTP == nil {
				continue
			}
			for _, p := range rule.HTTP.Paths {
				backend := ""
				if p.Backend.Service != nil {
					port := p.Backend.Service.Port.Name
					if p.Backend.Service.Port.Number != 0 {
						port = fmt.Sprint(p.Backend.Service.Port.Number)
					}
					backend = p.Backend.Service.Name + ":" + port
				}
				s.Paths = append(s.Paths, fmt.Sprintf("%s%s -> %s", host, p.Path, backend))
			}
		}
		for _, lb := range ing.Status.LoadBalancer.Ingress {
			if lb.IP != "" {
				s.Addresses = append(s.Addresses, lb.IP)
			} else if lb.Hostname != "" {
				s.Addresses = append(s.Addresses, lb.Hostname)
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Manager) ListNetworkPolicies(id int64, namespaces []string) ([]NetworkPolicySummary, error) {
	conn, err := m.getOptionalConn(id, KindNetworkPolicies)
	if err != nil {
		return nil, err
	}
	items, err := listNamespaced(namespaces, func(ns string) ([]*networkingv1.NetworkPolicy, error) {
		return conn.opt.networkPolicies.NetworkPolicies(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list networkpolicies: %w", err)
	}
	out := make([]NetworkPolicySummary, 0, len(items))
	for _, np := range items {
		sel := labels.Set(np.Spec.PodSelector.MatchLabels).String()
		if sel == "" {
			sel = "<all>"
		}
		types := make([]string, 0, len(np.Spec.PolicyTypes))
		for _, pt := range np.Spec.PolicyTypes {
			types = append(types, string(pt))
		}
		out = append(out, NetworkPolicySummary{Name: np.Name, Namespace: np.Namespace, PodSelector: sel, PolicyTypes: types, CreatedAt: np.CreationTimestamp.Time})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func accessModes(modes []corev1.PersistentVolumeAccessMode) []string {
	out := make([]string, 0, len(modes))
	for _, m := range modes {
		switch m {
		case corev1.ReadWriteOnce:
			out = append(out, "RWO")
		case corev1.ReadOnlyMany:
			out = append(out, "ROX")
		case corev1.ReadWriteMany:
			out = append(out, "RWX")
		case corev1.ReadWriteOncePod:
			out = append(out, "RWOP")
		default:
			out = append(out, string(m))
		}
	}
	return out
}

func (m *Manager) ListPVCs(id int64, namespaces []string) ([]PVCSummary, error) {
	conn, err := m.getOptionalConn(id, KindPVCs)
	if err != nil {
		return nil, err
	}
	items, err := listNamespaced(namespaces, func(ns string) ([]*corev1.PersistentVolumeClaim, error) {
		return conn.opt.pvcs.PersistentVolumeClaims(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list pvcs: %w", err)
	}
	out := make([]PVCSummary, 0, len(items))
	for _, p := range items {
		s := PVCSummary{Name: p.Name, Namespace: p.Namespace, Status: string(p.Status.Phase), Volume: p.Spec.VolumeName, AccessModes: accessModes(p.Spec.AccessModes), CreatedAt: p.CreationTimestamp.Time}
		if q, ok := p.Status.Capacity[corev1.ResourceStorage]; ok {
			s.Capacity = q.String()
		} else if q, ok := p.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			s.Capacity = q.String()
		}
		if p.Spec.StorageClassName != nil {
			s.StorageClass = *p.Spec.StorageClassName
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Manager) ListPVs(id int64) ([]PVSummary, error) {
	conn, err := m.getOptionalConn(id, KindPVs)
	if err != nil {
		return nil, err
	}
	items, err := conn.opt.pvs.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list pvs: %w", err)
	}
	out := make([]PVSummary, 0, len(items))
	for _, p := range items {
		s := PVSummary{Name: p.Name, Status: string(p.Status.Phase), AccessModes: accessModes(p.Spec.AccessModes), ReclaimPolicy: string(p.Spec.PersistentVolumeReclaimPolicy), StorageClass: p.Spec.StorageClassName, CreatedAt: p.CreationTimestamp.Time}
		if q, ok := p.Spec.Capacity[corev1.ResourceStorage]; ok {
			s.Capacity = q.String()
		}
		if p.Spec.ClaimRef != nil {
			s.Claim = p.Spec.ClaimRef.Namespace + "/" + p.Spec.ClaimRef.Name
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Manager) ListStorageClasses(id int64) ([]StorageClassSummary, error) {
	conn, err := m.getOptionalConn(id, KindStorageClasses)
	if err != nil {
		return nil, err
	}
	items, err := conn.opt.storageClasses.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list storageclasses: %w", err)
	}
	out := make([]StorageClassSummary, 0, len(items))
	for _, sc := range items {
		s := StorageClassSummary{Name: sc.Name, Provisioner: sc.Provisioner, CreatedAt: sc.CreationTimestamp.Time,
			IsDefault: sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true"}
		if sc.ReclaimPolicy != nil {
			s.ReclaimPolicy = string(*sc.ReclaimPolicy)
		}
		if sc.VolumeBindingMode != nil {
			s.VolumeBindingMode = string(*sc.VolumeBindingMode)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// jobStatus condenses a Job's conditions into a single stable state.
func jobStatus(j *batchv1.Job) string {
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return "Complete"
		case batchv1.JobFailed:
			return "Failed"
		case batchv1.JobSuspended:
			return "Suspended"
		}
	}
	if j.Status.Active > 0 {
		return "Running"
	}
	return "Pending"
}

func (m *Manager) ListJobs(id int64, namespaces []string) ([]JobSummary, error) {
	conn, err := m.getOptionalConn(id, KindJobs)
	if err != nil {
		return nil, err
	}
	items, err := listNamespaced(namespaces, func(ns string) ([]*batchv1.Job, error) {
		return conn.opt.jobs.Jobs(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list jobs: %w", err)
	}
	out := make([]JobSummary, 0, len(items))
	for _, j := range items {
		s := JobSummary{Name: j.Name, Namespace: j.Namespace, Status: jobStatus(j), Succeeded: j.Status.Succeeded, Failed: j.Status.Failed, Active: j.Status.Active, Completions: 1, CreatedAt: j.CreationTimestamp.Time}
		if j.Spec.Completions != nil {
			s.Completions = *j.Spec.Completions
		}
		if j.Status.StartTime != nil {
			t := j.Status.StartTime.Time
			s.StartedAt = &t
			end := time.Now()
			if j.Status.CompletionTime != nil {
				end = j.Status.CompletionTime.Time
			}
			s.Duration = end.Sub(t).Truncate(time.Second).String()
		}
		out = append(out, s)
	}
	// Most recent first: with jobs what matters is the last thing that ran.
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *Manager) ListCronJobs(id int64, namespaces []string) ([]CronJobSummary, error) {
	conn, err := m.getOptionalConn(id, KindCronJobs)
	if err != nil {
		return nil, err
	}
	items, err := listNamespaced(namespaces, func(ns string) ([]*batchv1.CronJob, error) {
		return conn.opt.cronJobs.CronJobs(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list cronjobs: %w", err)
	}
	out := make([]CronJobSummary, 0, len(items))
	for _, cj := range items {
		s := CronJobSummary{Name: cj.Name, Namespace: cj.Namespace, Schedule: cj.Spec.Schedule, Active: len(cj.Status.Active), CreatedAt: cj.CreationTimestamp.Time}
		if cj.Spec.Suspend != nil {
			s.Suspended = *cj.Spec.Suspend
		}
		if cj.Status.LastScheduleTime != nil {
			t := cj.Status.LastScheduleTime.Time
			s.LastSchedule = &t
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func hpaMetric(spec autoscalingv2.MetricSpec, status []autoscalingv2.MetricStatus) string {
	switch spec.Type {
	case autoscalingv2.ResourceMetricSourceType:
		if spec.Resource == nil {
			return ""
		}
		target := "?"
		if spec.Resource.Target.AverageUtilization != nil {
			target = fmt.Sprintf("%d%%", *spec.Resource.Target.AverageUtilization)
		} else if spec.Resource.Target.AverageValue != nil {
			target = spec.Resource.Target.AverageValue.String()
		}
		current := "?"
		for _, st := range status {
			if st.Resource != nil && st.Resource.Name == spec.Resource.Name {
				if st.Resource.Current.AverageUtilization != nil {
					current = fmt.Sprintf("%d%%", *st.Resource.Current.AverageUtilization)
				} else if st.Resource.Current.AverageValue != nil {
					current = st.Resource.Current.AverageValue.String()
				}
			}
		}
		return fmt.Sprintf("%s %s/%s", spec.Resource.Name, current, target)
	case autoscalingv2.PodsMetricSourceType:
		if spec.Pods != nil {
			return "pods:" + spec.Pods.Metric.Name
		}
	case autoscalingv2.ObjectMetricSourceType:
		if spec.Object != nil {
			return "object:" + spec.Object.Metric.Name
		}
	case autoscalingv2.ExternalMetricSourceType:
		if spec.External != nil {
			return "external:" + spec.External.Metric.Name
		}
	}
	return strings.ToLower(string(spec.Type))
}

func (m *Manager) ListHPAs(id int64, namespaces []string) ([]HPASummary, error) {
	conn, err := m.getOptionalConn(id, KindHPAs)
	if err != nil {
		return nil, err
	}
	items, err := listNamespaced(namespaces, func(ns string) ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
		return conn.opt.hpas.HorizontalPodAutoscalers(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, fmt.Errorf("k8s: failed to list hpas: %w", err)
	}
	out := make([]HPASummary, 0, len(items))
	for _, h := range items {
		s := HPASummary{Name: h.Name, Namespace: h.Namespace, Target: h.Spec.ScaleTargetRef.Kind + "/" + h.Spec.ScaleTargetRef.Name, Metrics: []string{},
			MinReplicas: 1, MaxReplicas: h.Spec.MaxReplicas, Current: h.Status.CurrentReplicas, Desired: h.Status.DesiredReplicas, CreatedAt: h.CreationTimestamp.Time}
		if h.Spec.MinReplicas != nil {
			s.MinReplicas = *h.Spec.MinReplicas
		}
		for _, ms := range h.Spec.Metrics {
			if line := hpaMetric(ms, h.Status.CurrentMetrics); line != "" {
				s.Metrics = append(s.Metrics, line)
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
