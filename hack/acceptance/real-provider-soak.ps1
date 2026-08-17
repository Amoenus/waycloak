# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$OutputPath,
    [string]$Namespace = "applications-media",
    [string]$Gateway = "waycloak-proton",
    [string]$Lease = "qbittorrent",
    [string]$ExpectedVersion = "v0.1.0-rc.11",
    [string]$ExpectedManifestDigest = "sha256:2d3b8cbf732ca7f15953085f2a954dbea9a9e1141d2f88d68f794e4265265c50",
    [ValidateRange(1, 720)]
    [int]$DurationHours = 72,
    [ValidateRange(15, 3600)]
    [int]$IntervalSeconds = 60,
    [ValidateRange(1, 1440)]
    [int]$ExternalProbeEverySamples = 10,
    [ValidateRange(0, 1000000)]
    [int]$MaxSamples = 0
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

function Invoke-KubectlJSON {
    param([string[]]$Arguments)
    $raw = & kubectl @Arguments 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw [System.InvalidOperationException]::new("kubectl query failed")
    }
    return ($raw | ConvertFrom-Json)
}

function Get-ConditionMap {
    param($Resource)
    $result = [ordered]@{}
    foreach ($condition in @($Resource.status.conditions)) {
        $result[$condition.type] = "{0}/{1}" -f $condition.status, $condition.reason
    }
    return $result
}

function Get-PodReady {
    param($Pod)
    if ($null -eq $Pod) { return $false }
    $statuses = @($Pod.status.containerStatuses)
    return $statuses.Count -gt 0 -and @($statuses | Where-Object { -not $_.ready }).Count -eq 0
}

function Get-Restarts {
    param($Pod)
    if ($null -eq $Pod) { return -1 }
    return [int](@($Pod.status.containerStatuses | ForEach-Object { [int]$_.restartCount }) | Measure-Object -Sum).Sum
}

function Select-Pod {
    param($Pods, [string]$Prefix)
    return @($Pods | Where-Object { $_.metadata.name.StartsWith($Prefix) } | Sort-Object { $_.metadata.name })[0]
}

function Test-QBittorrentListener {
    param([string]$PodName, [int]$Port, [string]$Protocol)
    if ([string]::IsNullOrWhiteSpace($PodName) -or $Port -le 0) { return $false }
    $pattern = if ($Protocol -eq "tcp") { "^tcp.*:$Port[[:space:]]" } else { "^udp.*:$Port[[:space:]]" }
    $netstatFlags = if ($Protocol -eq "tcp") { "-lnt" } else { "-lnu" }
    & kubectl -n $Namespace exec $PodName -c qbittorrent -- sh -c "netstat $netstatFlags 2>/dev/null | grep -Eq '$pattern'" 2>$null | Out-Null
    return $LASTEXITCODE -eq 0
}

function Test-PodDNS {
    param([string]$PodName, [string]$Name)
    if ([string]::IsNullOrWhiteSpace($PodName)) { return $false }
    & kubectl -n $Namespace exec $PodName -c qbittorrent -- nslookup $Name 2>$null | Out-Null
    return $LASTEXITCODE -eq 0
}

function Write-JSONLine {
    param($Value)
    $line = $Value | ConvertTo-Json -Depth 12 -Compress
    [System.IO.File]::AppendAllText($OutputPath, $line + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
}

$parent = Split-Path -Parent $OutputPath
if ($parent) { [System.IO.Directory]::CreateDirectory($parent) | Out-Null }
if (Test-Path -LiteralPath $OutputPath) {
    throw "output already exists; use a new path so soak epochs cannot be merged"
}

$started = [DateTimeOffset]::UtcNow
$deadline = $started.AddHours($DurationHours)
$baseline = @{}
$previous = @{}
$summary = [ordered]@{
    samples = 0
    collectionFailures = 0
    releaseIdentityFailures = 0
    gatewayNotReadySamples = 0
    gatewayDNSNotReadySamples = 0
    leaseNotReadySamples = 0
    workloadNotReadySamples = 0
    adapterNotReadySamples = 0
    restartIncreases = 0
    unexpectedUIDChanges = 0
    providerMappingChanges = 0
    listenerTCPFailures = 0
    listenerUDPFailures = 0
    externalDNSFailures = 0
    clusterDNSFailures = 0
    externalTCPProbeSuccesses = 0
    externalTCPProbeFailures = 0
    gatewayResourceVersionChanges = 0
    leaseResourceVersionChanges = 0
}

Write-JSONLine ([ordered]@{
    kind = "WaycloakLocalSoakStart"
    apiVersion = "evidence.waycloak.io/v1"
    startedAt = $started.ToString("O")
    deadline = $deadline.ToString("O")
    intervalSeconds = $IntervalSeconds
    expectedVersion = $ExpectedVersion
    expectedManifestDigest = $ExpectedManifestDigest
    namespace = $Namespace
    canary = "qbittorrent"
    publicEndpointRecorded = $false
})

while ([DateTimeOffset]::UtcNow -lt $deadline) {
    $sampleStarted = [DateTimeOffset]::UtcNow
    $summary.samples++
    try {
        $gatewayResource = Invoke-KubectlJSON @("-n", $Namespace, "get", "vpngateway", $Gateway, "-o", "json")
        $leaseResource = Invoke-KubectlJSON @("-n", $Namespace, "get", "portforwardlease", $Lease, "-o", "json")
        $podList = Invoke-KubectlJSON @("-n", $Namespace, "get", "pods", "-o", "json")
        $pods = @($podList.items)
        $gatewayPod = Select-Pod $pods ("waycloak-gateway-{0}-" -f $Gateway)
        $workloadPod = @($pods | Where-Object { $_.metadata.name -match '^qbittorrent-[a-f0-9]+-' -and $_.metadata.name -notmatch 'adapter' } | Sort-Object { $_.metadata.name })[0]
        $adapterPod = Select-Pod $pods "qbittorrent-waycloak-adapter-"

        $gatewayConditions = Get-ConditionMap $gatewayResource
        $leaseConditions = Get-ConditionMap $leaseResource
        $gatewayReady = $gatewayConditions["Ready"] -like "True/*"
        $gatewayDNSReady = $gatewayConditions["DNSReady"] -like "True/*"
        $leaseReady = $leaseConditions["Ready"] -like "True/*"
        $workloadReady = Get-PodReady $workloadPod
        $adapterReady = Get-PodReady $adapterPod
        $gatewayPodReady = Get-PodReady $gatewayPod
        $port = [int]$leaseResource.status.provider.publicPort
        $listenerTCP = Test-QBittorrentListener $workloadPod.metadata.name $port "tcp"
        $listenerUDP = Test-QBittorrentListener $workloadPod.metadata.name $port "udp"
        $externalDNS = Test-PodDNS $workloadPod.metadata.name "example.com"
        $clusterDNS = Test-PodDNS $workloadPod.metadata.name "kubernetes.default.svc.cluster.local"

        $releaseVersion = [string]$gatewayPod.metadata.annotations.'runtime.networking.waycloak.io/release-version'
        $releaseManifest = [string]$gatewayPod.metadata.annotations.'runtime.networking.waycloak.io/release-manifest-digest'
        $releaseExact = $releaseVersion -eq $ExpectedVersion -and $releaseManifest -eq $ExpectedManifestDigest

        $mappingIdentity = "{0}:{1}" -f $leaseResource.status.provider.publicAddress, $port
        if (-not $baseline.ContainsKey("mapping")) { $baseline.mapping = $mappingIdentity }
        $mappingChanged = $mappingIdentity -ne $baseline.mapping
        if ($mappingChanged -and $mappingIdentity -ne $previous.mapping) { $summary.providerMappingChanges++ }

        foreach ($entry in @(
            @{ key = "gatewayPodUID"; value = [string]$gatewayPod.metadata.uid },
            @{ key = "workloadPodUID"; value = [string]$workloadPod.metadata.uid },
            @{ key = "adapterPodUID"; value = [string]$adapterPod.metadata.uid }
        )) {
            if (-not $baseline.ContainsKey($entry.key)) { $baseline[$entry.key] = $entry.value }
        }
        $uidChanged = $baseline.gatewayPodUID -ne [string]$gatewayPod.metadata.uid -or
            $baseline.workloadPodUID -ne [string]$workloadPod.metadata.uid -or
            $baseline.adapterPodUID -ne [string]$adapterPod.metadata.uid
        if ($uidChanged -and -not $previous.uidChanged) { $summary.unexpectedUIDChanges++ }

        $restarts = [ordered]@{
            gateway = Get-Restarts $gatewayPod
            workload = Get-Restarts $workloadPod
            adapter = Get-Restarts $adapterPod
        }
        $restartIncrease = $false
        if ($previous.ContainsKey("restarts")) {
            $restartIncrease = $restarts.gateway -gt $previous.restarts.gateway -or
                $restarts.workload -gt $previous.restarts.workload -or
                $restarts.adapter -gt $previous.restarts.adapter
        }
        if ($restartIncrease) { $summary.restartIncreases++ }

        $gatewayRVChanged = $previous.gatewayRV -and $previous.gatewayRV -ne [string]$gatewayResource.metadata.resourceVersion
        $leaseRVChanged = $previous.leaseRV -and $previous.leaseRV -ne [string]$leaseResource.metadata.resourceVersion
        if ($gatewayRVChanged) { $summary.gatewayResourceVersionChanges++ }
        if ($leaseRVChanged) { $summary.leaseResourceVersionChanges++ }

        $externalTCP = $null
        if ((($summary.samples - 1) % $ExternalProbeEverySamples) -eq 0 -and $port -gt 0) {
            try {
                $probe = Invoke-RestMethod -Method Post -Uri "https://portchecker.io/api/query" -ContentType "application/json" -Body (@{ host = [string]$leaseResource.status.provider.publicAddress; ports = @($port) } | ConvertTo-Json -Compress) -TimeoutSec 15
                $externalTCP = [bool]$probe.check[0].status
                if ($externalTCP) { $summary.externalTCPProbeSuccesses++ } else { $summary.externalTCPProbeFailures++ }
            } catch {
                $externalTCP = $null
                $summary.externalTCPProbeFailures++
            }
        }

        if (-not $releaseExact) { $summary.releaseIdentityFailures++ }
        if (-not $gatewayReady -or -not $gatewayPodReady) { $summary.gatewayNotReadySamples++ }
        if (-not $gatewayDNSReady) { $summary.gatewayDNSNotReadySamples++ }
        if (-not $leaseReady) { $summary.leaseNotReadySamples++ }
        if (-not $workloadReady) { $summary.workloadNotReadySamples++ }
        if (-not $adapterReady) { $summary.adapterNotReadySamples++ }
        if (-not $listenerTCP) { $summary.listenerTCPFailures++ }
        if (-not $listenerUDP) { $summary.listenerUDPFailures++ }
        if (-not $externalDNS) { $summary.externalDNSFailures++ }
        if (-not $clusterDNS) { $summary.clusterDNSFailures++ }

        Write-JSONLine ([ordered]@{
            kind = "WaycloakLocalSoakSample"
            apiVersion = "evidence.waycloak.io/v1"
            observedAt = $sampleStarted.ToString("O")
            collectionHealthy = $true
            releaseExact = $releaseExact
            gatewayReady = $gatewayReady
            gatewayDNSReady = $gatewayDNSReady
            gatewayPodReady = $gatewayPodReady
            leaseReady = $leaseReady
            workloadReady = $workloadReady
            adapterReady = $adapterReady
            listenerTCP = $listenerTCP
            listenerUDP = $listenerUDP
            externalDNS = $externalDNS
            clusterDNS = $clusterDNS
            externalTCP = $externalTCP
            mappingChangedFromStart = $mappingChanged
            uidChangedFromStart = $uidChanged
            restartIncrease = $restartIncrease
            gatewayResourceVersionChanged = [bool]$gatewayRVChanged
            leaseResourceVersionChanged = [bool]$leaseRVChanged
            providerExpiry = ([DateTimeOffset]$leaseResource.status.provider.expiresAt).ToString("O")
            handoffGeneration = [int64]$leaseResource.status.handoffGeneration
            gatewayConditions = $gatewayConditions
            leaseConditions = $leaseConditions
            restarts = $restarts
            publicEndpointRecorded = $false
        })

        $previous.mapping = $mappingIdentity
        $previous.uidChanged = $uidChanged
        $previous.restarts = $restarts
        $previous.gatewayRV = [string]$gatewayResource.metadata.resourceVersion
        $previous.leaseRV = [string]$leaseResource.metadata.resourceVersion
    } catch {
        $summary.collectionFailures++
        Write-JSONLine ([ordered]@{
            kind = "WaycloakLocalSoakSample"
            apiVersion = "evidence.waycloak.io/v1"
            observedAt = $sampleStarted.ToString("O")
            collectionHealthy = $false
            failureCategory = $_.Exception.GetType().Name
            publicEndpointRecorded = $false
        })
    }

    if ($MaxSamples -gt 0 -and $summary.samples -ge $MaxSamples) { break }

    $remaining = ($deadline - [DateTimeOffset]::UtcNow).TotalSeconds
    if ($remaining -gt 0) {
        Start-Sleep -Seconds ([Math]::Min($IntervalSeconds, [Math]::Ceiling($remaining)))
    }
}

Write-JSONLine ([ordered]@{
    kind = "WaycloakLocalSoakSummary"
    apiVersion = "evidence.waycloak.io/v1"
    startedAt = $started.ToString("O")
    completedAt = [DateTimeOffset]::UtcNow.ToString("O")
    expectedVersion = $ExpectedVersion
    expectedManifestDigest = $ExpectedManifestDigest
    publicEndpointRecorded = $false
    counters = $summary
})
