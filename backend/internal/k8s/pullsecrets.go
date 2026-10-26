package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

// SetImagePullSecret adds (attach=true) or removes (attach=false) an
// imagePullSecret from the given ServiceAccount, impersonating the user. It is
// done with get+update and retry on conflict, NOT with server-side apply: in
// the API `imagePullSecrets` is an atomic list, and an apply with Force would
// overwrite the secrets another manager (Helm, an operator) had added. If the SA
// does not exist yet (freshly created namespace) we wait for the controller to
// create it: it is not created here so as not to fight with it.
func (m *Manager) SetImagePullSecret(ctx context.Context, id int64, namespace, serviceAccount, secretName string, attach bool, actingUser, actingGroup string) error {
	if err := m.GuardNamespace(id, namespace); err != nil {
		return err
	}
	conn, err := m.getConn(id)
	if err != nil {
		return err
	}
	cs, err := conn.actionClientset(actingUser, actingGroup)
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		sa, err := cs.CoreV1().ServiceAccounts(namespace).Get(ctx, serviceAccount, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("ServiceAccount %s/%s does not exist yet", namespace, serviceAccount)
			}
			return fmt.Errorf("k8s: failed to read ServiceAccount %s/%s: %w", namespace, serviceAccount, err)
		}
		idx := -1
		for i, ref := range sa.ImagePullSecrets {
			if ref.Name == secretName {
				idx = i
				break
			}
		}
		switch {
		case attach && idx >= 0, !attach && idx < 0:
			return nil // already as requested
		case attach:
			sa.ImagePullSecrets = append(sa.ImagePullSecrets, corev1.LocalObjectReference{Name: secretName})
		default:
			sa.ImagePullSecrets = append(sa.ImagePullSecrets[:idx], sa.ImagePullSecrets[idx+1:]...)
		}
		if _, err := cs.CoreV1().ServiceAccounts(namespace).Update(ctx, sa, metav1.UpdateOptions{FieldManager: fieldManager}); err != nil {
			if apierrors.IsConflict(err) {
				return err
			}
			return fmt.Errorf("k8s: failed to update ServiceAccount %s/%s: %w", namespace, serviceAccount, err)
		}
		return nil
	})
}
