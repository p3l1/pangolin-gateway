// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/gateway"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// Keys of the generated Secret. They are the ones kubernetes.io/basic-auth
// prescribes, so the API server rejects a Secret of that type missing either.
const (
	basicAuthUserKey     = "username"
	basicAuthPasswordKey = "password"
)

// passwordBytes of entropy, encoded base64url so the credential survives a
// URL's userinfo unescaped — which is how a webhook sender carries it.
const passwordBytes = 24

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create

// resolveBasicAuth returns the credentials for every served route that asks to
// be protected, creating the Secret that holds them the first time. An error
// aborts the pass: a route whose credentials are momentarily unreadable must
// not be rendered without them, and rendering it away would unpublish it.
func (p *Publisher) resolveBasicAuth(
	ctx context.Context,
	routes []gatewayv1.HTTPRoute,
) (map[types.NamespacedName]gateway.BasicAuthResult, error) {
	var resolved map[types.NamespacedName]gateway.BasicAuthResult

	for _, r := range routes {
		// An unreadable annotation is the renderer's to report on the route.
		if requested, err := gateway.BasicAuthRequested(r); err != nil || !requested {
			continue
		}
		result, err := p.basicAuthFor(ctx, r)
		if err != nil {
			return nil, err
		}
		if resolved == nil {
			resolved = map[types.NamespacedName]gateway.BasicAuthResult{}
		}
		resolved[types.NamespacedName{Namespace: r.Namespace, Name: r.Name}] = result
	}
	return resolved, nil
}

func (p *Publisher) basicAuthFor(
	ctx context.Context,
	r gatewayv1.HTTPRoute,
) (gateway.BasicAuthResult, error) {
	name := types.NamespacedName{
		Namespace: r.Namespace,
		Name:      gateway.BasicAuthSecretName(r.Name),
	}
	if msgs := validation.IsDNS1123Subdomain(name.Name); len(msgs) > 0 {
		return refused("a Secret cannot be named %q after this route: %s", name.Name, msgs[0]), nil
	}

	var secret corev1.Secret
	err := p.Client.Get(ctx, name, &secret)
	switch {
	case apierrors.IsNotFound(err):
		if p.DryRun {
			return refused("dry run: Secret %s would be created to hold them", name), nil
		}
		return p.createBasicAuthSecret(ctx, r, name)
	case err != nil:
		return gateway.BasicAuthResult{}, fmt.Errorf("reading Secret %s: %w", name, err)
	}
	return credentialsFrom(secret, r)
}

// createBasicAuthSecret writes the credential once. The password is never
// rewritten afterwards, so the URL an operator copied out keeps working; a
// rotation is a deletion, which brings this path back.
func (p *Publisher) createBasicAuthSecret(
	ctx context.Context,
	r gatewayv1.HTTPRoute,
	name types.NamespacedName,
) (gateway.BasicAuthResult, error) {
	password, err := randomPassword()
	if err != nil {
		return gateway.BasicAuthResult{}, fmt.Errorf("generating a password for %s: %w", name, err)
	}

	controller := true
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: name.Namespace,
			Name:      name.Name,
			// The route owns the Secret, so Kubernetes removes it with the route
			// and the controller needs no permission to delete one.
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: gatewayv1.GroupVersion.String(),
				Kind:       "HTTPRoute",
				Name:       r.Name,
				UID:        r.UID,
				Controller: &controller,
			}},
		},
		Type: corev1.SecretTypeBasicAuth,
		StringData: map[string]string{
			basicAuthUserKey:     r.Name,
			basicAuthPasswordKey: password,
		},
	}
	if err := p.Client.Create(ctx, secret); err != nil {
		return gateway.BasicAuthResult{}, fmt.Errorf("creating Secret %s: %w", name, err)
	}

	return gateway.BasicAuthResult{Credentials: &pangolin.BasicAuth{
		User:     r.Name,
		Password: password,
	}}, nil
}

// credentialsFrom reads a Secret the controller expects to own. A Secret that
// belongs to something else is refused rather than adopted: its contents would
// otherwise become the password of a resource on the internet.
func credentialsFrom(
	secret corev1.Secret,
	r gatewayv1.HTTPRoute,
) (gateway.BasicAuthResult, error) {
	if !ownedBy(secret, r) {
		return refused("Secret %s/%s is not owned by this route; delete it or rename the route",
			secret.Namespace, secret.Name), nil
	}

	user := string(secret.Data[basicAuthUserKey])
	password := string(secret.Data[basicAuthPasswordKey])
	if user == "" || password == "" {
		return refused("Secret %s/%s has no %s and %s; delete it to have one generated",
			secret.Namespace, secret.Name, basicAuthUserKey, basicAuthPasswordKey), nil
	}

	return gateway.BasicAuthResult{Credentials: &pangolin.BasicAuth{
		User:     user,
		Password: password,
	}}, nil
}

func ownedBy(secret corev1.Secret, r gatewayv1.HTTPRoute) bool {
	for _, ref := range secret.OwnerReferences {
		if ref.UID == r.UID && ref.Kind == "HTTPRoute" {
			return true
		}
	}
	return false
}

func randomPassword() (string, error) {
	raw := make([]byte, passwordBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func refused(format string, args ...any) gateway.BasicAuthResult {
	return gateway.BasicAuthResult{Reason: fmt.Sprintf(format, args...)}
}
