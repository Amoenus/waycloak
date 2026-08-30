# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$OutputPath,
    [string]$Namespace = "applications-media",
    [string]$Gateway = "waycloak-proton",
    [string]$Lease = "qbittorrent",
    [Parameter(Mandatory = $true)]
    [string]$ExpectedVersion,
    [Parameter(Mandatory = $true)]
    [string]$ExpectedManifestDigest,
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

function Start-GatewayTransitionWatch {
    param([string]$WatchNamespace, [string]$WatchGateway, [string]$WatchPath)
    return Start-Job -ScriptBlock {
        param($Namespace, $Gateway, $Path)
        $encoding = [System.Text.UTF8Encoding]::new($false)
        & kubectl -n $Namespace get vpngateway $Gateway --watch --output-watch-events -o=json 2>$null | ForEach-Object {
            try {
                $event = $_ | ConvertFrom-Json
                $resource = $event.object
                $conditions = [ordered]@{}
                foreach ($condition in @($resource.status.conditions)) {
                    $conditions[$condition.type] = [ordered]@{
                        status = [string]$condition.status
                        reason = [string]$condition.reason
                        lastTransitionTime = [string]$condition.lastTransitionTime
                    }
                }
                $record = [ordered]@{
                    kind = "WaycloakGatewayTransition"
                    apiVersion = "evidence.waycloak.io/v1"
                    observedAt = [DateTimeOffset]::UtcNow.ToString("O")
                    eventType = [string]$event.type
                    gatewayUID = [string]$resource.metadata.uid
                    resourceVersion = [string]$resource.metadata.resourceVersion
                    gatewayReadyStatus = [string]$conditions.Ready.status
                    gatewayDNSReadyStatus = [string]$conditions.DNSReady.status
                    conditions = $conditions
                    publicEndpointRecorded = $false
                }
                $line = $record | ConvertTo-Json -Depth 12 -Compress
                [System.IO.File]::AppendAllText($Path, $line + [Environment]::NewLine, $encoding)
            } catch {
                $record = [ordered]@{
                    kind = "WaycloakGatewayTransitionCollectionError"
                    apiVersion = "evidence.waycloak.io/v1"
                    observedAt = [DateTimeOffset]::UtcNow.ToString("O")
                    failureCategory = $_.Exception.GetType().Name
                    publicEndpointRecorded = $false
                }
                $line = $record | ConvertTo-Json -Compress
                [System.IO.File]::AppendAllText($Path, $line + [Environment]::NewLine, $encoding)
            }
        }
    } -ArgumentList $WatchNamespace, $WatchGateway, $WatchPath
}

$parent = Split-Path -Parent $OutputPath
if ($parent) { [System.IO.Directory]::CreateDirectory($parent) | Out-Null }
if (Test-Path -LiteralPath $OutputPath) {
    throw "output already exists; use a new path so soak epochs cannot be merged"
}
$transitionPath = $OutputPath + ".gateway-transitions.tmp"
if (Test-Path -LiteralPath $transitionPath) {
    throw "gateway transition evidence already exists; use a new output path"
}
$transitionJob = Start-GatewayTransitionWatch -WatchNamespace $Namespace -WatchGateway $Gateway -WatchPath $transitionPath
$watchReadyDeadline = [DateTimeOffset]::UtcNow.AddSeconds(10)
while ([DateTimeOffset]::UtcNow -lt $watchReadyDeadline -and
    (-not (Test-Path -LiteralPath $transitionPath) -or (Get-Item -LiteralPath $transitionPath).Length -eq 0)) {
    Start-Sleep -Milliseconds 100
}
if (-not (Test-Path -LiteralPath $transitionPath) -or (Get-Item -LiteralPath $transitionPath).Length -eq 0) {
    Stop-Job -Job $transitionJob
    Receive-Job -Job $transitionJob | Out-Null
    Remove-Job -Job $transitionJob
    throw "gateway transition watch did not become ready"
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
    handoffGenerationChanges = 0
    listenerTCPFailures = 0
    listenerUDPFailures = 0
    externalDNSFailures = 0
    clusterDNSFailures = 0
    externalTCPProbeSuccesses = 0
    externalTCPProbeFailures = 0
    gatewayResourceVersionChanges = 0
    leaseResourceVersionChanges = 0
    gatewayReadyTransitions = 0
    gatewayReadyWithdrawals = 0
    gatewayDNSReadyTransitions = 0
    gatewayDNSReadyWithdrawals = 0
    gatewayTransitionCollectionFailures = 0
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
    $samplePhase = "resource_read"
    try {
        $gatewayResource = Invoke-KubectlJSON @("-n", $Namespace, "get", "vpngateway", $Gateway, "-o", "json")
        $leaseResource = Invoke-KubectlJSON @("-n", $Namespace, "get", "portforwardlease", $Lease, "-o", "json")
        $podList = Invoke-KubectlJSON @("-n", $Namespace, "get", "pods", "-o", "json")
        $samplePhase = "pod_selection"
        $pods = @($podList.items)
        $gatewayPod = Select-Pod $pods ("waycloak-gateway-{0}-" -f $Gateway)
        $workloadPod = @($pods | Where-Object { $_.metadata.name -match '^qbittorrent-[a-f0-9]+-' -and $_.metadata.name -notmatch 'adapter' } | Sort-Object { $_.metadata.name })[0]
        $adapterPod = Select-Pod $pods "qbittorrent-waycloak-adapter-"

        $samplePhase = "condition_observation"
        $gatewayConditions = Get-ConditionMap $gatewayResource
        $leaseConditions = Get-ConditionMap $leaseResource
        $gatewayReady = $gatewayConditions["Ready"] -like "True/*"
        $gatewayDNSReady = $gatewayConditions["DNSReady"] -like "True/*"
        $leaseReady = $leaseConditions["Ready"] -like "True/*"
        $workloadReady = Get-PodReady $workloadPod
        $adapterReady = Get-PodReady $adapterPod
        $gatewayPodReady = Get-PodReady $gatewayPod
        $providerStatus = $null
        if ($null -ne $leaseResource.status -and $leaseResource.status.PSObject.Properties.Name -contains "provider") {
            $providerStatus = $leaseResource.status.provider
        }
        $port = 0
        $providerAddress = ""
        if ($null -ne $providerStatus) {
            $port = [int]$providerStatus.publicPort
            $providerAddress = [string]$providerStatus.publicAddress
        }
        $samplePhase = "listener_observation"
        $listenerTCP = Test-QBittorrentListener $workloadPod.metadata.name $port "tcp"
        $listenerUDP = Test-QBittorrentListener $workloadPod.metadata.name $port "udp"
        $samplePhase = "dns_observation"
        $externalDNS = Test-PodDNS $workloadPod.metadata.name "example.com"
        $clusterDNS = Test-PodDNS $workloadPod.metadata.name "kubernetes.default.svc.cluster.local"

        $samplePhase = "release_identity"
        $releaseVersion = [string]$gatewayPod.metadata.annotations.'runtime.networking.waycloak.io/release-version'
        $releaseManifest = [string]$gatewayPod.metadata.annotations.'runtime.networking.waycloak.io/release-manifest-digest'
        $releaseExact = $releaseVersion -eq $ExpectedVersion -and $releaseManifest -eq $ExpectedManifestDigest

        $mappingIdentity = "{0}:{1}" -f $providerAddress, $port
        if (-not $baseline.ContainsKey("mapping")) { $baseline.mapping = $mappingIdentity }
        $mappingChanged = $mappingIdentity -ne $baseline.mapping
        if ($mappingChanged -and $mappingIdentity -ne $previous.mapping) { $summary.providerMappingChanges++ }

        $handoffGeneration = [int64]$leaseResource.status.handoffGeneration
        if (-not $baseline.ContainsKey("handoffGeneration")) { $baseline.handoffGeneration = $handoffGeneration }
        $handoffGenerationChanged = $handoffGeneration -ne $baseline.handoffGeneration
        if ($handoffGenerationChanged -and $handoffGeneration -ne $previous.handoffGeneration) {
            $summary.handoffGenerationChanges++
        }

        foreach ($entry in @(
            @{ key = "gatewayResourceUID"; value = [string]$gatewayResource.metadata.uid },
            @{ key = "leaseResourceUID"; value = [string]$leaseResource.metadata.uid },
            @{ key = "gatewayPodUID"; value = [string]$gatewayPod.metadata.uid },
            @{ key = "workloadPodUID"; value = [string]$workloadPod.metadata.uid },
            @{ key = "adapterPodUID"; value = [string]$adapterPod.metadata.uid }
        )) {
            if (-not $baseline.ContainsKey($entry.key)) { $baseline[$entry.key] = $entry.value }
        }
        $uidChanged = $baseline.gatewayResourceUID -ne [string]$gatewayResource.metadata.uid -or
            $baseline.leaseResourceUID -ne [string]$leaseResource.metadata.uid -or
            $baseline.gatewayPodUID -ne [string]$gatewayPod.metadata.uid -or
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

        $samplePhase = "external_tcp"
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

        $samplePhase = "evidence_serialization"
        $providerExpiry = $null
        if ($null -ne $providerStatus) {
            $providerExpiryValue = [string]$providerStatus.expiresAt
        } else {
            $providerExpiryValue = ""
        }
        if ($providerExpiryValue) {
            $parsedProviderExpiry = [DateTimeOffset]::MinValue
            if (-not [DateTimeOffset]::TryParse($providerExpiryValue, [ref]$parsedProviderExpiry)) {
                throw "provider expiry is not a valid timestamp"
            }
            $providerExpiry = $parsedProviderExpiry.ToString("O")
        }
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
            handoffGenerationChangedFromStart = $handoffGenerationChanged
            uidChangedFromStart = $uidChanged
            restartIncrease = $restartIncrease
            gatewayResourceVersionChanged = [bool]$gatewayRVChanged
            leaseResourceVersionChanged = [bool]$leaseRVChanged
            providerExpiry = $providerExpiry
            handoffGeneration = $handoffGeneration
            gatewayConditions = $gatewayConditions
            leaseConditions = $leaseConditions
            restarts = $restarts
            publicEndpointRecorded = $false
        })

        $previous.mapping = $mappingIdentity
        $previous.handoffGeneration = $handoffGeneration
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
            failurePhase = $samplePhase
            publicEndpointRecorded = $false
        })
    }

    if ($MaxSamples -gt 0 -and $summary.samples -ge $MaxSamples) { break }

    $remaining = ($deadline - [DateTimeOffset]::UtcNow).TotalSeconds
    if ($remaining -gt 0) {
        Start-Sleep -Seconds ([Math]::Min($IntervalSeconds, [Math]::Ceiling($remaining)))
    }
}

Stop-Job -Job $transitionJob
Receive-Job -Job $transitionJob | Out-Null
Remove-Job -Job $transitionJob

$previousGatewayReadyStatus = $null
$previousGatewayDNSReadyStatus = $null
if (Test-Path -LiteralPath $transitionPath) {
    foreach ($line in Get-Content -LiteralPath $transitionPath) {
        [System.IO.File]::AppendAllText($OutputPath, $line + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
        $transition = $line | ConvertFrom-Json
        if ($transition.kind -eq "WaycloakGatewayTransitionCollectionError") {
            $summary.gatewayTransitionCollectionFailures++
            continue
        }
        $gatewayReadyStatus = [string]$transition.gatewayReadyStatus
        $gatewayDNSReadyStatus = [string]$transition.gatewayDNSReadyStatus
        if ($null -ne $previousGatewayReadyStatus -and $gatewayReadyStatus -ne $previousGatewayReadyStatus) {
            $summary.gatewayReadyTransitions++
            if ($gatewayReadyStatus -ne "True") { $summary.gatewayReadyWithdrawals++ }
        }
        if ($null -ne $previousGatewayDNSReadyStatus -and $gatewayDNSReadyStatus -ne $previousGatewayDNSReadyStatus) {
            $summary.gatewayDNSReadyTransitions++
            if ($gatewayDNSReadyStatus -ne "True") { $summary.gatewayDNSReadyWithdrawals++ }
        }
        $previousGatewayReadyStatus = $gatewayReadyStatus
        $previousGatewayDNSReadyStatus = $gatewayDNSReadyStatus
    }
    Remove-Item -LiteralPath $transitionPath -Force
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
