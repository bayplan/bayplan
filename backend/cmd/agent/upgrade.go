package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// The agent updates itself when the server publishes a new version: the server
// sends in /api/agent/rbac the image it expects (the one with the same tag as
// itself) and, if it differs from the running one (BAYPLAN_IMAGE from the
// manifest), the agent changes the image of its own Deployment and Kubernetes
// restarts it. It only accepts another TAG of the same repository: a
// compromised server cannot make the cluster pull an arbitrary image.

var imageTagRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// upgradeTarget decides whether current should be replaced by desired. Pure.
func upgradeTarget(current, desired string) (string, bool, error) {
	desired = strings.TrimSpace(desired)
	if desired == "" || current == "" || desired == current {
		return "", false, nil
	}
	curRepo, _ := splitImage(current)
	desRepo, desTag := splitImage(desired)
	if curRepo == "" || desRepo != curRepo {
		return "", false, fmt.Errorf("image %q outside the current repository %q", desired, curRepo)
	}
	if desTag == "" || !imageTagRe.MatchString(desTag) {
		return "", false, fmt.Errorf("invalid tag in %q", desired)
	}
	return desired, true, nil
}

// splitImage separates repository and tag; a digest (@sha256:…) is not
// accepted as a target (it cannot be compared with a tag) and returns an empty
// tag.
func splitImage(ref string) (repo, tag string) {
	if strings.Contains(ref, "@") {
		return strings.SplitN(ref, "@", 2)[0], ""
	}
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i+1:], "/") {
		return ref, "latest"
	}
	return ref[:i], ref[i+1:]
}

// upgradePatch is the strategic merge patch for the "agent" container: the
// image AND the BAYPLAN_IMAGE variable (env merges by name, so the other
// variables stay). Patching only the image left BAYPLAN_IMAGE at the old
// value, so the new pod believed it was still on the old image: it patched
// again on every sync and, if an operator pinned another image by hand, it
// overwrote it with the server's. Pure.
func upgradePatch(image string) []byte {
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []map[string]any{{
			"name":  "agent",
			"image": image,
			"env":   []map[string]any{{"name": "BAYPLAN_IMAGE", "value": image}},
		}},
	}}}})
	return patch
}

// upgradeSelf patches the bayplan-agent Deployment (see upgradePatch). After
// this the current pod dies.
func (s *rbacSyncer) upgradeSelf(ctx context.Context, image string) error {
	tok, err := s.token()
	if err != nil {
		return err
	}
	patch := upgradePatch(image)
	url := fmt.Sprintf("https://%s/apis/apps/v1/namespaces/bayplan-agent/deployments/bayplan-agent", s.apiHostPort)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(patch))
	req.Header.Set("Content-Type", "application/strategic-merge-patch+json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := s.kube.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return errors.New(strings.TrimSpace(string(b)))
	}
	return nil
}
