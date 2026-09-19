# Security Policy

## Supported versions

The latest released minor version receives security fixes. Older versions do not.

## Reporting a vulnerability

Report privately through GitHub's advisory form:
https://github.com/p3l1/pangolin-gateway/security/advisories/new

Please do not open a public issue for a suspected vulnerability. Expect an
acknowledgement within seven days.

## What this project ships

Every release publishes an SPDX SBOM, build provenance, and a keyless cosign
signature.

### Verify the image signature

    cosign verify ghcr.io/p3l1/pangolin-gateway:<version> \
      --certificate-identity-regexp '^https://github.com/p3l1/pangolin-gateway/' \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com

### Verify the SBOM attestation

    cosign verify-attestation --type spdxjson \
      --certificate-identity-regexp '^https://github.com/p3l1/pangolin-gateway/' \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com \
      ghcr.io/p3l1/pangolin-gateway:<version>

### Verify the build provenance

    gh attestation verify oci://ghcr.io/p3l1/pangolin-gateway:<version> \
      --repo p3l1/pangolin-gateway
