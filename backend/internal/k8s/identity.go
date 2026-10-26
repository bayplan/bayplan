package k8s

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ErrPanelIdentity: an object would run as, or mint a token for, one of the
// panel's own ServiceAccounts. Those identities impersonate the bayplan:*
// groups and any username, so a workload running as them would give an
// editor the panel's reach outside its audit. The API server enforces the
// same rule with deploy/rbac/admission-policy.yaml (1.30+); this check is the
// one that also holds on older clusters and answers before anything is sent.
var ErrPanelIdentity = errors.New("the panel's own ServiceAccounts cannot be used by workloads applied through the panel")

// panelServiceAccounts: the direct connector's technical SA and the agent's.
var panelServiceAccounts = map[string]bool{"bayplan-technical": true, AgentNamespace: true}

// podSpecPaths: where each workload kind keeps its pod spec.
var podSpecPaths = map[string][]string{
	"Pod":         {"spec"},
	"Deployment":  {"spec", "template", "spec"},
	"StatefulSet": {"spec", "template", "spec"},
	"DaemonSet":   {"spec", "template", "spec"},
	"ReplicaSet":  {"spec", "template", "spec"},
	"Job":         {"spec", "template", "spec"},
	"CronJob":     {"spec", "jobTemplate", "spec", "template", "spec"},
}

// GuardPanelIdentity fails for a workload whose pod spec names a panel SA and
// for a service-account-token Secret bound to one. Pure.
func GuardPanelIdentity(obj *unstructured.Unstructured) error {
	kind := obj.GetKind()
	if path, ok := podSpecPaths[kind]; ok {
		for _, field := range []string{"serviceAccountName", "serviceAccount"} {
			sa, _, _ := unstructured.NestedString(obj.Object, append(append([]string{}, path...), field)...)
			if panelServiceAccounts[sa] {
				return fmt.Errorf("%w (%s %s uses %s)", ErrPanelIdentity, kind, obj.GetName(), sa)
			}
		}
		return nil
	}
	if kind == "Secret" {
		typ, _, _ := unstructured.NestedString(obj.Object, "type")
		sa := obj.GetAnnotations()["kubernetes.io/service-account.name"]
		if typ == "kubernetes.io/service-account-token" && panelServiceAccounts[sa] {
			return fmt.Errorf("%w (Secret %s is a token for %s)", ErrPanelIdentity, obj.GetName(), sa)
		}
	}
	return nil
}
