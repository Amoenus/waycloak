import v1alpha1 as networking

adapter = networking.WorkloadAdapter {
    metadata = {name = "sample"}
    spec = {
        protocolVersion = "networking.waycloak.io/adapter/v1alpha1"
        image = "ghcr.io/example/adapter@sha256:0000000000000000000000000000000000000000000000000000000000000000"
    }
}

items = [adapter]
