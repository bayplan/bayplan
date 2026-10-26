package k8s

import (
	"errors"
	"testing"
)

func TestGuardPanelIdentity(t *testing.T) {
	cases := map[string]struct {
		yaml string
		deny bool
	}{
		"pod as technical SA":        {"apiVersion: v1\nkind: Pod\nmetadata: {name: p}\nspec: {serviceAccountName: bayplan-technical, containers: [{name: c, image: x}]}\n", true},
		"pod with deprecated field":  {"apiVersion: v1\nkind: Pod\nmetadata: {name: p}\nspec: {serviceAccount: bayplan-agent, containers: [{name: c, image: x}]}\n", true},
		"deployment as agent SA":     {"apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: d}\nspec: {template: {spec: {serviceAccountName: bayplan-agent}}}\n", true},
		"cronjob as technical SA":    {"apiVersion: batch/v1\nkind: CronJob\nmetadata: {name: c}\nspec: {jobTemplate: {spec: {template: {spec: {serviceAccountName: bayplan-technical}}}}}\n", true},
		"token secret for panel SA":  {"apiVersion: v1\nkind: Secret\nmetadata: {name: s, annotations: {kubernetes.io/service-account.name: bayplan-technical}}\ntype: kubernetes.io/service-account-token\n", true},
		"deployment with its own SA": {"apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: d}\nspec: {template: {spec: {serviceAccountName: app}}}\n", false},
		"token secret for other SA":  {"apiVersion: v1\nkind: Secret\nmetadata: {name: s, annotations: {kubernetes.io/service-account.name: app}}\ntype: kubernetes.io/service-account-token\n", false},
		"opaque secret":              {"apiVersion: v1\nkind: Secret\nmetadata: {name: s, annotations: {kubernetes.io/service-account.name: bayplan-technical}}\ntype: Opaque\n", false},
		"configmap":                  {"apiVersion: v1\nkind: ConfigMap\nmetadata: {name: c}\ndata: {serviceAccountName: bayplan-technical}\n", false},
	}
	for name, c := range cases {
		objs, err := ParseManifests(c.yaml)
		if err != nil || len(objs) != 1 {
			t.Fatalf("%s: parse: %v", name, err)
		}
		err = GuardPanelIdentity(objs[0])
		if c.deny != errors.Is(err, ErrPanelIdentity) {
			t.Errorf("%s: deny=%v, got %v", name, c.deny, err)
		}
	}
}
