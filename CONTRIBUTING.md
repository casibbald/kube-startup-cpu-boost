# How to Contribute

We'd love to accept your patches and contributions to this project. There are
just a few small guidelines you need to follow.

## Contributor License Agreement

Contributions to this project must be accompanied by a Contributor License
Agreement. You (or your employer) retain the copyright to your contribution;
this simply gives us permission to use and redistribute your contributions as
part of the project. Head over to <https://cla.developers.google.com/> to see
your current agreements on file or to sign a new one.

You generally only need to submit a CLA once, so if you've already submitted one
(even if it was for a different project), you probably don't need to do it
again.

## Code Reviews

All submissions, including submissions by project members, require review. We
use GitHub pull requests for this purpose. Consult
[GitHub Help](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/proposing-changes-to-your-work-with-pull-requests/about-pull-requests/)
for more information on using pull requests.

## Community Guidelines

This project follows [Google's Open Source Community
Guidelines](https://opensource.google/conduct/).

## Development Workflow

### Updating API Types and CRDs

When you modify the API types in `api/v1alpha1/startupcpuboost_types.go`:

1. **Generate CRDs**: Run `make manifests` to generate the CRD in `config/crd/bases/`
2. **Sync Helm Chart CRD**: Run `make sync-helm-crd` to update the Helm chart CRD template
   - This ensures the Helm chart CRD template stays in sync with the generated CRD
   - The script preserves Helm-specific metadata (labels, annotations, template syntax)
3. **Commit both files**: Commit both the generated CRD and the Helm chart CRD template

**Important Notes**:
- **Never manually edit** `charts/kube-startup-cpu-boost/templates/startupcpuboost-crd.yaml` directly
  - It contains Helm template syntax (e.g., `{{- include ... }}`) and must be processed by Helm
  - Manual edits will break YAML parsing and cause errors
  - Always use `make sync-helm-crd` to update it from the generated CRD
- The Helm chart CRD template must be kept in sync with the generated CRD (`config/crd/bases/autoscaling.x-k8s.io_startupcpuboosts.yaml`)
- The `make sync-helm-crd` target automates this process and preserves Helm template syntax
- Tilt automatically runs `make sync-helm-crd` after `make manifests` to keep them in sync during development
