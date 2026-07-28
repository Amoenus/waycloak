# Getting started

A supported turnkey journey is not published yet. Issues #138–#141 own the signed installer, preflight, clean-install smoke tests, support matrix, lifecycle runbooks, and exact-artifact certification.

Do not install the removed alpha runtime or reuse alpha objects. Do not purge an existing alpha installation from generic uninstall instructions. The future clean-break procedure will stop protected workloads behind the old deny path, enumerate and explicitly confirm exact destructive targets, purge alpha CRs and CRDs, install fresh replacement artifacts, author new manifests, and verify protected and unprotected traffic.

For development, use the replacement CRDs and API-only chart only in a disposable cluster. They do not constitute a complete gateway data plane or supported installation. See the [stable product plan](implementation/stable-product-plan.md) and [project status](../PROJECT_STATUS.md).
