# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$CanonicalPath,
    [Parameter(Mandatory = $true)]
    [string]$DHTPath,
    [Parameter(Mandatory = $true)]
    [string]$ConditionDirectory,
    [Parameter(Mandatory = $true)]
    [string]$HeartbeatPath,
    [Parameter(Mandatory = $true)]
    [string]$MetricsPath,
    [Parameter(Mandatory = $true)]
    [string]$ExpectedVersion,
    [Parameter(Mandatory = $true)]
    [string]$ExpectedManifestDigest,
    [switch]$AllowIncomplete
)

$ErrorActionPreference = "Stop"

function Read-JSONLines {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "evidence file is missing: $Path"
    }
    $records = [System.Collections.Generic.List[object]]::new()
    $lineNumber = 0
    foreach ($line in Get-Content -LiteralPath $Path) {
        $lineNumber++
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try {
            $records.Add(($line | ConvertFrom-Json -DateKind String))
        } catch {
            throw "invalid JSON line $lineNumber in $Path"
        }
    }
    return $records.ToArray()
}

function Count-NonTrueConditions {
    param([object[]]$Records, [string]$Kind)
    $count = 0
    foreach ($record in @($Records | Where-Object { $_.kind -eq $Kind })) {
        foreach ($condition in @($record.conditions.psobject.Properties.Value)) {
            if ($condition.status -ne "True") {
                $count++
                break
            }
        }
    }
    return $count
}

function Get-ObservedAtRange {
    param([object[]]$Records)
    $times = @($Records | Where-Object { $_.observedAt } | ForEach-Object {
        [DateTimeOffset]::Parse([string]$_.observedAt)
    } | Sort-Object)
    if ($times.Count -eq 0) {
        return [pscustomobject]@{ first = $null; last = $null; hours = 0.0; maximumGapSeconds = $null }
    }
    $maximumGap = 0.0
    for ($index = 1; $index -lt $times.Count; $index++) {
        $gap = ($times[$index] - $times[$index - 1]).TotalSeconds
        if ($gap -gt $maximumGap) { $maximumGap = $gap }
    }
    return [pscustomobject]@{
        first = $times[0].ToString("O")
        last = $times[-1].ToString("O")
        hours = if ($times.Count -gt 1) { ($times[-1] - $times[0]).TotalHours } else { 0.0 }
        maximumGapSeconds = if ($times.Count -gt 1) { $maximumGap } else { $null }
    }
}

function Get-RatePerHour {
    param([int]$Count, [double]$Hours)
    if ($Hours -le 0) { return $null }
    return $Count / $Hours
}

$leasePath = Join-Path $ConditionDirectory "portforwardlease.jsonl"
$bindingPath = Join-Path $ConditionDirectory "vpnworkloadbinding.jsonl"
$canonical = @(Read-JSONLines $CanonicalPath)
$dht = @(Read-JSONLines $DHTPath)
$lease = @(Read-JSONLines $leasePath)
$binding = @(Read-JSONLines $bindingPath)
$heartbeat = @(Read-JSONLines $HeartbeatPath)
$metrics = @(Read-JSONLines $MetricsPath)

$start = @($canonical | Where-Object { $_.kind -eq "WaycloakLocalSoakStart" })
$summary = @($canonical | Where-Object { $_.kind -eq "WaycloakLocalSoakSummary" })
if ($start.Count -ne 1) { throw "canonical evidence must contain exactly one start record" }
if ($start[0].expectedVersion -ne $ExpectedVersion -or
    $start[0].expectedManifestDigest -ne $ExpectedManifestDigest) {
    throw "canonical release identity does not match the expected release"
}

$canonicalSamples = @($canonical | Where-Object { $_.kind -eq "WaycloakLocalSoakSample" })
$canonicalFailures = @($canonicalSamples | Where-Object {
    -not $_.collectionHealthy -or
    -not $_.releaseExact -or
    -not $_.gatewayReady -or
    -not $_.gatewayDNSReady -or
    -not $_.gatewayPodReady -or
    -not $_.leaseReady -or
    -not $_.workloadReady -or
    -not $_.adapterReady -or
    -not $_.listenerTCP -or
    -not $_.listenerUDP -or
    -not $_.externalDNS -or
    -not $_.clusterDNS -or
    $_.mappingChangedFromStart -or
    $_.handoffGenerationChangedFromStart -or
    $_.uidChangedFromStart -or
    $_.restartIncrease -or
    ($null -ne $_.externalTCP -and -not $_.externalTCP)
})

$externalTCPChecks = @($canonicalSamples | Where-Object { $null -ne $_.externalTCP })
$externalTCPFailures = @($externalTCPChecks | Where-Object { -not $_.externalTCP })

$gatewayTransitions = @($canonical | Where-Object { $_.kind -eq "WaycloakGatewayTransition" })
$gatewayTransitionErrors = @($canonical | Where-Object { $_.kind -eq "WaycloakGatewayTransitionCollectionError" })
$transitionTemporaryPath = $CanonicalPath + ".gateway-transitions.tmp"
if ($summary.Count -eq 0 -and (Test-Path -LiteralPath $transitionTemporaryPath -PathType Leaf)) {
    $temporaryTransitions = @(Read-JSONLines $transitionTemporaryPath)
    $gatewayTransitions = @($temporaryTransitions | Where-Object { $_.kind -eq "WaycloakGatewayTransition" })
    $gatewayTransitionErrors = @($temporaryTransitions | Where-Object {
        $_.kind -eq "WaycloakGatewayTransitionCollectionError"
    })
}
$gatewayNonTrue = @($gatewayTransitions | Where-Object {
    $_.gatewayReadyStatus -ne "True" -or $_.gatewayDNSReadyStatus -ne "True"
})

$dhtSamples = @($dht | Where-Object { $_.kind -eq "WaycloakLocalSoakSample" })
$dhtFailures = @($dht | Where-Object { $_.kind -eq "WaycloakLocalSoakCollectionFailure" })
$dhtBadSamples = @($dhtSamples | Where-Object {
    -not $_.collectionHealthy -or
    -not $_.releaseExact -or
    -not $_.gatewayReady -or
    -not $_.gatewayDNSReady -or
    -not $_.gatewayPodReady -or
    -not $_.leaseReady -or
    -not $_.workloadReady -or
    -not $_.adapterReady -or
    -not $_.listenerTCP -or
    -not $_.listenerUDP -or
    -not $_.externalDNS -or
    -not $_.clusterDNS -or
    $_.handoffGenerationChangedFromStart -or
    $_.uidChangedFromStart -or
    $_.restartIncrease -or
    ($null -ne $_.externalTCP -and -not $_.externalTCP) -or
    ($null -ne $_.qBittorrentConnected -and -not $_.qBittorrentConnected) -or
    ($null -ne $_.qBittorrentDHTEnabled -and -not $_.qBittorrentDHTEnabled) -or
    ($null -ne $_.qBittorrentDHTNodes -and [int]$_.qBittorrentDHTNodes -le 0) -or
    ($null -ne $_.qBittorrentListenPortMatches -and -not $_.qBittorrentListenPortMatches) -or
    ($null -ne $_.qBittorrentUPnPDisabled -and -not $_.qBittorrentUPnPDisabled)
})
$dhtAPIObservations = @($dhtSamples | Where-Object { $_.qBittorrentAPIObserved })

$leaseParseErrors = @($lease | Where-Object { $_.kind -eq "WaycloakConditionWatchParseError" })
$bindingParseErrors = @($binding | Where-Object { $_.kind -eq "WaycloakConditionWatchParseError" })
$heartbeatParseErrors = @($heartbeat | Where-Object { $_.kind -eq "WaycloakBindingHeartbeatParseError" })
$leaseNonTrue = Count-NonTrueConditions $lease "WaycloakPortForwardLeaseTransition"
$bindingNonTrue = Count-NonTrueConditions $binding "WaycloakVPNWorkloadBindingTransition"
$heartbeatNonTrue = Count-NonTrueConditions $heartbeat "WaycloakVPNWorkloadBindingHeartbeat"

$metricSamples = @($metrics | Where-Object { $_.kind -eq "WaycloakMetricsTimelineSample" })
$metricFailures = @($metrics | Where-Object { $_.kind -eq "WaycloakMetricsTimelineFailure" })
$metricBadSamples = @($metricSamples | Where-Object {
    -not $_.collectionHealthy -or [int]$_.privacyCanaryMatches -ne 0
})

$expectedMetricFamilies = @(
    "waycloak_enrolled_pods",
    "waycloak_metrics_collection_success",
    "waycloak_resource_condition_objects",
    "waycloak_resources",
    "waycloak_workload_allocations"
)
$metricFamilyDrift = @($metricSamples | Where-Object {
    Compare-Object $expectedMetricFamilies @($_.metricFamilies) -SyncWindow 0
})

$canonicalStart = [DateTimeOffset]::Parse([string]$start[0].startedAt)
$canonicalDeadline = [DateTimeOffset]::Parse([string]$start[0].deadline)
$canonicalDurationHours = ($canonicalDeadline - $canonicalStart).TotalHours
$completedAt = if ($summary.Count -eq 1) {
    [DateTimeOffset]::Parse([string]$summary[0].completedAt)
} else {
    $null
}

$leaseRange = Get-ObservedAtRange $lease
$bindingRange = Get-ObservedAtRange $binding
$heartbeatRange = Get-ObservedAtRange $heartbeat
$metricRange = Get-ObservedAtRange $metricSamples

$complete = $summary.Count -eq 1 -and
    $null -ne $completedAt -and
    $completedAt -ge $canonicalDeadline -and
    $canonicalDurationHours -ge 72.0 -and
    @($dht | Where-Object { $_.kind -eq "WaycloakLocalSoakEnd" }).Count -eq 1 -and
    @($metrics | Where-Object { $_.kind -eq "WaycloakMetricsTimelineEnd" }).Count -eq 1 -and
    $null -ne $leaseRange.last -and [DateTimeOffset]::Parse($leaseRange.last) -ge $canonicalDeadline -and
    $null -ne $bindingRange.last -and [DateTimeOffset]::Parse($bindingRange.last) -ge $canonicalDeadline -and
    $null -ne $heartbeatRange.last -and [DateTimeOffset]::Parse($heartbeatRange.last) -ge $canonicalDeadline -and
    $null -ne $metricRange.last -and [DateTimeOffset]::Parse($metricRange.last) -ge $canonicalDeadline

$summaryFailures = 0
if ($summary.Count -eq 1) {
    foreach ($name in @(
        "collectionFailures", "releaseIdentityFailures", "gatewayNotReadySamples",
        "gatewayDNSNotReadySamples", "leaseNotReadySamples", "workloadNotReadySamples",
        "adapterNotReadySamples", "restartIncreases", "unexpectedUIDChanges",
        "providerMappingChanges", "handoffGenerationChanges", "listenerTCPFailures",
        "listenerUDPFailures", "externalDNSFailures", "clusterDNSFailures",
        "externalTCPProbeFailures", "gatewayReadyWithdrawals",
        "gatewayDNSReadyWithdrawals", "gatewayTransitionCollectionFailures"
    )) {
        if ([int64]$summary[0].counters.$name -ne 0) { $summaryFailures++ }
    }
}

$observedFailures = $canonicalFailures.Count + $externalTCPFailures.Count +
    $gatewayTransitionErrors.Count + $gatewayNonTrue.Count + $dhtFailures.Count +
    $dhtBadSamples.Count + $leaseParseErrors.Count + $bindingParseErrors.Count +
    $heartbeatParseErrors.Count + $leaseNonTrue + $bindingNonTrue + $heartbeatNonTrue +
    $metricFailures.Count + $metricBadSamples.Count + $metricFamilyDrift.Count +
    $summaryFailures

$report = [ordered]@{
    apiVersion = "evidence.waycloak.io/v1"
    kind = "WaycloakRealProviderSoakAudit"
    auditedAt = [DateTimeOffset]::UtcNow.ToString("O")
    expectedVersion = $ExpectedVersion
    expectedManifestDigest = $ExpectedManifestDigest
    complete = $complete
    healthySoFar = $observedFailures -eq 0
    canonical = [ordered]@{
        startedAt = $canonicalStart.ToString("O")
        deadline = $canonicalDeadline.ToString("O")
        durationHours = $canonicalDurationHours
        completedAt = if ($null -ne $completedAt) { $completedAt.ToString("O") } else { $null }
        samples = $canonicalSamples.Count
        badSamples = $canonicalFailures.Count
        externalTCPChecks = $externalTCPChecks.Count
        externalTCPFailures = $externalTCPFailures.Count
        gatewayTransitions = $gatewayTransitions.Count
        gatewayNonTrue = $gatewayNonTrue.Count
        gatewayTransitionErrors = $gatewayTransitionErrors.Count
        terminalSummaryFailures = $summaryFailures
        sha256 = (Get-FileHash -LiteralPath $CanonicalPath -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    qBittorrent = [ordered]@{
        samples = $dhtSamples.Count
        badSamples = $dhtBadSamples.Count
        collectionFailures = $dhtFailures.Count
        apiObservations = $dhtAPIObservations.Count
        minimumDHTNodes = if ($dhtAPIObservations.Count -gt 0) {
            ($dhtAPIObservations | Measure-Object qBittorrentDHTNodes -Minimum).Minimum
        } else { $null }
        sha256 = (Get-FileHash -LiteralPath $DHTPath -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    conditions = [ordered]@{
        leaseEvents = @($lease | Where-Object { $_.kind -eq "WaycloakPortForwardLeaseTransition" }).Count
        leaseNonTrue = $leaseNonTrue
        leaseRange = $leaseRange
        leaseEventsPerHour = Get-RatePerHour $lease.Count $leaseRange.hours
        bindingEvents = @($binding | Where-Object { $_.kind -eq "WaycloakVPNWorkloadBindingTransition" }).Count
        bindingNonTrue = $bindingNonTrue
        bindingRange = $bindingRange
        bindingEventsPerHour = Get-RatePerHour $binding.Count $bindingRange.hours
        heartbeatEvents = @($heartbeat | Where-Object { $_.kind -eq "WaycloakVPNWorkloadBindingHeartbeat" }).Count
        heartbeatNonTrue = $heartbeatNonTrue
        heartbeatRange = $heartbeatRange
        heartbeatEventsPerHour = Get-RatePerHour $heartbeat.Count $heartbeatRange.hours
        leaseSHA256 = (Get-FileHash -LiteralPath $leasePath -Algorithm SHA256).Hash.ToLowerInvariant()
        bindingSHA256 = (Get-FileHash -LiteralPath $bindingPath -Algorithm SHA256).Hash.ToLowerInvariant()
        heartbeatSHA256 = (Get-FileHash -LiteralPath $HeartbeatPath -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    metrics = [ordered]@{
        samples = $metricSamples.Count
        badSamples = $metricBadSamples.Count
        collectionFailures = $metricFailures.Count
        familyDrift = $metricFamilyDrift.Count
        range = $metricRange
        sha256 = (Get-FileHash -LiteralPath $MetricsPath -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    observedFailures = $observedFailures
    publicEndpointRecorded = $false
}

$report | ConvertTo-Json -Depth 12

if ($observedFailures -ne 0) { exit 1 }
if (-not $complete -and -not $AllowIncomplete) { exit 2 }
