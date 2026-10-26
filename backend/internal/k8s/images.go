package k8s

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// ImageUsage is a container image in use in the cluster and where.
type ImageUsage struct {
	Image string `json:"image"`
	// Digest resolved by the kubelet (status.containerStatuses[].imageID), if
	// any replica reports it: it is what is really running even if the tag is
	// mutable.
	Digest    string   `json:"digest,omitempty"`
	Pods      int      `json:"pods"`
	Workloads []string `json:"workloads"` // "Deployment ns/name"; "Pod ns/name" if it has no owner
}

// ClusterImages groups by image the containers (and initContainers) of the
// non-terminated pods, with the owning workload resolved via ReplicaSet just
// like in the rest of the panel. From the informer cache, no calls to the
// cluster.
func (m *Manager) ClusterImages(id int64, namespaces []string) ([]ImageUsage, error) {
	conn, err := m.getSyncedConn(id)
	if err != nil {
		return nil, err
	}
	pods, err := listNamespaced(namespaces, func(ns string) ([]*corev1.Pod, error) {
		return conn.podLister.Pods(ns).List(labels.Everything())
	})
	if err != nil {
		return nil, err
	}
	type acc struct {
		digest    string
		pods      map[string]bool
		workloads map[string]bool
	}
	byImage := map[string]*acc{}
	add := func(image, digest, workload, pod string) {
		a := byImage[image]
		if a == nil {
			a = &acc{pods: map[string]bool{}, workloads: map[string]bool{}}
			byImage[image] = a
		}
		a.pods[pod] = true
		a.workloads[workload] = true
		if a.digest == "" && strings.Contains(digest, "@sha256:") {
			a.digest = digest[strings.Index(digest, "@")+1:]
		}
	}
	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		workload := podOwner(conn, p)
		digests := map[string]string{}
		for _, st := range append(append([]corev1.ContainerStatus{}, p.Status.ContainerStatuses...), p.Status.InitContainerStatuses...) {
			digests[st.Name] = st.ImageID
		}
		for _, c := range append(append([]corev1.Container{}, p.Spec.Containers...), p.Spec.InitContainers...) {
			add(c.Image, digests[c.Name], workload, p.Namespace+"/"+p.Name)
		}
	}
	out := make([]ImageUsage, 0, len(byImage))
	for image, a := range byImage {
		w := make([]string, 0, len(a.workloads))
		for k := range a.workloads {
			w = append(w, k)
		}
		sort.Strings(w)
		out = append(out, ImageUsage{Image: image, Digest: a.digest, Pods: len(a.pods), Workloads: w})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Image < out[j].Image })
	return out, nil
}

// podOwner returns "Kind ns/name" of a pod's owner (Deployment through the
// ReplicaSet, StatefulSet, DaemonSet, Job…), or the pod itself.
func podOwner(conn *clusterConn, p *corev1.Pod) string {
	for _, ref := range p.OwnerReferences {
		kind, name := ref.Kind, ref.Name
		if kind == "ReplicaSet" {
			if rs, err := conn.replicaSetLister.ReplicaSets(p.Namespace).Get(ref.Name); err == nil {
				for _, r := range rs.OwnerReferences {
					if r.Kind == "Deployment" {
						kind, name = r.Kind, r.Name
					}
				}
			}
		}
		return kind + " " + p.Namespace + "/" + name
	}
	return "Pod " + p.Namespace + "/" + p.Name
}
