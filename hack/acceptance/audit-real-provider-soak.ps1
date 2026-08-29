# Copyright 2026 The Waycloak Authors.
# SPDX-License-Identifier: MIT

#Requires -Version 7.5

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
    [string]$BoundedHandoffEvidencePath = "",
    [switch]$AllowIncomplete
)

$ErrorActionPreference = "Stop"

function Read-JSONLine {
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

function Measure-NonTrueCondition {
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
$canonical = @(Read-JSONLine $CanonicalPath)
$dht = @(Read-JSONLine $DHTPath)
$lease = @(Read-JSONLine $leasePath)
$binding = @(Read-JSONLine $bindingPath)
$heartbeat = @(Read-JSONLine $HeartbeatPath)
$metrics = @(Read-JSONLine $MetricsPath)

$start = @($canonical | Where-Object { $_.kind -eq "WaycloakLocalSoakStart" })
$summary = @($canonical | Where-Object { $_.kind -eq "WaycloakLocalSoakSummary" })
if ($start.Count -ne 1) { throw "canonical evidence must contain exactly one start record" }
if ($start[0].expectedVersion -ne $ExpectedVersion -or
    $start[0].expectedManifestDigest -ne $ExpectedManifestDigest) {
    throw "canonical release identity does not match the expected release"
}

$canonicalSamples = @($canonical | Where-Object { $_.kind -eq "WaycloakLocalSoakSample" })
$canonicalBaseFailures = @($canonicalSamples | Where-Object {
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
    $_.uidChangedFromStart -or
    $_.restartIncrease
})

$canonicalLifecycleSamples = @($canonicalSamples | Where-Object {
    $_.mappingChangedFromStart -or
    $_.handoffGenerationChangedFromStart -or
    ($null -ne $_.externalTCP -and -not $_.externalTCP)
})
$canonicalFailures = @($canonicalBaseFailures + $canonicalLifecycleSamples | Sort-Object observedAt -Unique)

$externalTCPChecks = @($canonicalSamples | Where-Object { $null -ne $_.externalTCP })
$externalTCPFailures = @($externalTCPChecks | Where-Object { -not $_.externalTCP })

$gatewayTransitions = @($canonical | Where-Object { $_.kind -eq "WaycloakGatewayTransition" })
$gatewayTransitionErrors = @($canonical | Where-Object { $_.kind -eq "WaycloakGatewayTransitionCollectionError" })
$transitionTemporaryPath = $CanonicalPath + ".gateway-transitions.tmp"
if ($summary.Count -eq 0 -and (Test-Path -LiteralPath $transitionTemporaryPath -PathType Leaf)) {
    $temporaryTransitions = @(Read-JSONLine $transitionTemporaryPath)
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
$leaseEvents = @($lease | Where-Object { $_.kind -eq "WaycloakPortForwardLeaseTransition" })
$bindingEvents = @($binding | Where-Object { $_.kind -eq "WaycloakVPNWorkloadBindingTransition" })
$heartbeatEvents = @($heartbeat | Where-Object { $_.kind -eq "WaycloakVPNWorkloadBindingHeartbeat" })
$leaseNonTrue = Measure-NonTrueCondition $leaseEvents "WaycloakPortForwardLeaseTransition"
$bindingNonTrue = Measure-NonTrueCondition $bindingEvents "WaycloakVPNWorkloadBindingTransition"
$heartbeatNonTrue = Measure-NonTrueCondition $heartbeatEvents "WaycloakVPNWorkloadBindingHeartbeat"

$boundedHandoff = [ordered]@{
    supplied = -not [string]::IsNullOrWhiteSpace($BoundedHandoffEvidencePath)
    valid = $false
    reason = if ([string]::IsNullOrWhiteSpace($BoundedHandoffEvidencePath)) { "NotSupplied" } else { "Invalid" }
    externalTCPFailures = 0
    leaseNonTrueEvents = 0
    bindingNonTrueEvents = 0
    heartbeatNonTrueEvents = 0
    lifecycleSamples = 0
    failureAt = $null
    recoveredAt = $null
    handoffGenerationBefore = $null
    handoffGenerationAfter = $null
    sha256 = $null
}

if ($boundedHandoff.supplied) {
    if (-not (Test-Path -LiteralPath $BoundedHandoffEvidencePath -PathType Leaf)) {
        throw "bounded handoff evidence file is missing: $BoundedHandoffEvidencePath"
    }
    try {
        $handoffEvidence = Get-Content -LiteralPath $BoundedHandoffEvidencePath -Raw |
            ConvertFrom-Json -DateKind String
    } catch {
        throw "bounded handoff evidence is invalid JSON"
    }
    if ($handoffEvidence.kind -ne "WaycloakBoundedProviderHandoffDiagnosis" -or
        $handoffEvidence.apiVersion -ne "evidence.waycloak.io/v1" -or
        $handoffEvidence.publicEndpointRecorded -ne $false -or
        $handoffEvidence.credentialRecorded -ne $false) {
        throw "bounded handoff evidence has an invalid identity or privacy boundary"
    }

    $failureAt = [DateTimeOffset]::Parse([string]$handoffEvidence.externalTCPFailureAt)
    $bindingWithdrawalAt = [DateTimeOffset]::Parse([string]$handoffEvidence.bindingWithdrawalFirst)
    $leaseWithdrawalAt = [DateTimeOffset]::Parse([string]$handoffEvidence.leaseWithdrawalFirst)
    $recoveredAt = [DateTimeOffset]::Parse([string]$handoffEvidence.firstRecoveredCanonicalSample)
    $beforeGeneration = [int64]$handoffEvidence.handoffGenerationBefore
    $afterGeneration = [int64]$handoffEvidence.handoffGenerationAfter
    $boundedHandoff.failureAt = $failureAt.ToString("O")
    $boundedHandoff.recoveredAt = $recoveredAt.ToString("O")
    $boundedHandoff.handoffGenerationBefore = $beforeGeneration
    $boundedHandoff.handoffGenerationAfter = $afterGeneration
    $boundedHandoff.sha256 = (Get-FileHash -LiteralPath $BoundedHandoffEvidencePath -Algorithm SHA256).Hash.ToLowerInvariant()

    $matchingExternalFailures = @($externalTCPFailures | Where-Object {
        [DateTimeOffset]::Parse([string]$_.observedAt) -eq $failureAt
    })
    $observedGenerations = @($canonicalSamples | ForEach-Object { [int64]$_.handoffGeneration } |
        Sort-Object -Unique)
    $postRecoverySamples = @($canonicalSamples | Where-Object {
        [DateTimeOffset]::Parse([string]$_.observedAt) -ge $recoveredAt
    })
    $postRecoveryFailures = @($postRecoverySamples | Where-Object {
        -not $_.collectionHealthy -or -not $_.releaseExact -or
        -not $_.gatewayReady -or -not $_.gatewayDNSReady -or -not $_.gatewayPodReady -or
        -not $_.leaseReady -or -not $_.workloadReady -or -not $_.adapterReady -or
        -not $_.listenerTCP -or -not $_.listenerUDP -or
        -not $_.externalDNS -or -not $_.clusterDNS -or
        $_.uidChangedFromStart -or $_.restartIncrease -or
        ($null -ne $_.externalTCP -and -not $_.externalTCP)
    })
    $leaseNonTrueEvents = @($leaseEvents | Where-Object {
        @($_.conditions.psobject.Properties.Value | Where-Object { $_.status -ne "True" }).Count -gt 0
    })
    $bindingNonTrueEvents = @($bindingEvents | Where-Object {
        @($_.conditions.psobject.Properties.Value | Where-Object { $_.status -ne "True" }).Count -gt 0
    })
    $heartbeatNonTrueEvents = @($heartbeatEvents | Where-Object {
        @($_.conditions.psobject.Properties.Value | Where-Object { $_.status -ne "True" }).Count -gt 0
    })
    $allNonTrueEvents = @($leaseNonTrueEvents + $bindingNonTrueEvents + $heartbeatNonTrueEvents)
    $outsideWindow = @($allNonTrueEvents | Where-Object {
        $observedAt = [DateTimeOffset]::Parse([string]$_.observedAt)
        $observedAt -lt $failureAt -or $observedAt -gt $recoveredAt
    })
    $lifecycleOutsideWindow = @($canonicalLifecycleSamples | Where-Object {
        $observedAt = [DateTimeOffset]::Parse([string]$_.observedAt)
        ($null -ne $_.externalTCP -and -not $_.externalTCP -and $observedAt -ne $failureAt) -or
        (($_.mappingChangedFromStart -or $_.handoffGenerationChangedFromStart) -and $observedAt -lt $failureAt)
    })

    $validHandoff =
        $externalTCPFailures.Count -eq 1 -and $matchingExternalFailures.Count -eq 1 -and
        $observedGenerations.Count -eq 2 -and
        $observedGenerations[0] -eq $beforeGeneration -and
        $observedGenerations[1] -eq $afterGeneration -and
        $afterGeneration -eq ($beforeGeneration + 1) -and
        $bindingWithdrawalAt -ge $failureAt -and
        ($bindingWithdrawalAt - $failureAt).TotalSeconds -le 30 -and
        $leaseWithdrawalAt -ge $bindingWithdrawalAt -and
        ($leaseWithdrawalAt - $failureAt).TotalSeconds -le 30 -and
        $recoveredAt -ge $leaseWithdrawalAt -and
        ($recoveredAt - $failureAt).TotalSeconds -le 120 -and
        $leaseNonTrueEvents.Count -gt 0 -and $bindingNonTrueEvents.Count -gt 0 -and
        $heartbeatNonTrueEvents.Count -gt 0 -and
        $outsideWindow.Count -eq 0 -and $lifecycleOutsideWindow.Count -eq 0 -and
        $canonicalBaseFailures.Count -eq 0 -and $postRecoveryFailures.Count -eq 0 -and
        $handoffEvidence.releaseIdentityChanged -eq $false -and
        $handoffEvidence.gitOpsRevisionChanged -eq $false -and
        $handoffEvidence.workloadUIDChanged -eq $false -and
        $handoffEvidence.gatewayUIDChanged -eq $false -and
        $handoffEvidence.adapterUIDChanged -eq $false -and
        $handoffEvidence.restartIncrease -eq $false -and
        [int]$handoffEvidence.postRecovery.directTCPAttempts -gt 0 -and
        [int]$handoffEvidence.postRecovery.directTCPSuccesses -eq
            [int]$handoffEvidence.postRecovery.directTCPAttempts -and
        [int]$handoffEvidence.postRecovery.externalCheckerAttempts -gt 0 -and
        [int]$handoffEvidence.postRecovery.externalCheckerSuccesses -eq
            [int]$handoffEvidence.postRecovery.externalCheckerAttempts -and
        $handoffEvidence.postRecovery.leaseConditionsTrue -eq $true -and
        $handoffEvidence.postRecovery.listenerAcknowledged -eq $true

    if ($validHandoff) {
        $boundedHandoff.valid = $true
        $boundedHandoff.reason = "BoundedAndRecovered"
        $boundedHandoff.externalTCPFailures = $matchingExternalFailures.Count
        $boundedHandoff.leaseNonTrueEvents = $leaseNonTrueEvents.Count
        $boundedHandoff.bindingNonTrueEvents = $bindingNonTrueEvents.Count
        $boundedHandoff.heartbeatNonTrueEvents = $heartbeatNonTrueEvents.Count
        $boundedHandoff.lifecycleSamples = $canonicalLifecycleSamples.Count
        $canonicalFailures = @($canonicalBaseFailures)
    }
}

$unexplainedExternalTCPFailures = $externalTCPFailures.Count - $boundedHandoff.externalTCPFailures
$unexplainedLeaseNonTrue = $leaseNonTrue - $boundedHandoff.leaseNonTrueEvents
$unexplainedBindingNonTrue = $bindingNonTrue - $boundedHandoff.bindingNonTrueEvents
$unexplainedHeartbeatNonTrue = $heartbeatNonTrue - $boundedHandoff.heartbeatNonTrueEvents

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
    $actualFamilies = @($_.metricFamilies | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) })
    if ($actualFamilies.Count -eq 0) {
        $true
    } else {
        @(Compare-Object ($expectedMetricFamilies | Sort-Object) ($actualFamilies | Sort-Object)).Count -ne 0
    }
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
        $counter = $summary[0].counters.psobject.Properties[$name]
        if ($null -eq $counter) {
            $summaryFailures++
            continue
        }
        $expectedValue = 0
        if ($boundedHandoff.valid -and $name -eq "handoffGenerationChanges") {
            $expectedValue = 1
        }
        if ($boundedHandoff.valid -and $name -eq "externalTCPProbeFailures") {
            $expectedValue = $boundedHandoff.externalTCPFailures
        }
        if ([int64]$counter.Value -ne $expectedValue) { $summaryFailures++ }
    }
}

$observedFailures = $canonicalFailures.Count + $unexplainedExternalTCPFailures +
    $gatewayTransitionErrors.Count + $gatewayNonTrue.Count + $dhtFailures.Count +
    $dhtBadSamples.Count + $leaseParseErrors.Count + $bindingParseErrors.Count +
    $heartbeatParseErrors.Count + $unexplainedLeaseNonTrue + $unexplainedBindingNonTrue +
    $unexplainedHeartbeatNonTrue +
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
        rawLifecycleSamples = $canonicalLifecycleSamples.Count
        externalTCPChecks = $externalTCPChecks.Count
        externalTCPFailures = $externalTCPFailures.Count
        unexplainedExternalTCPFailures = $unexplainedExternalTCPFailures
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
        leaseEvents = $leaseEvents.Count
        leaseNonTrue = $leaseNonTrue
        unexplainedLeaseNonTrue = $unexplainedLeaseNonTrue
        leaseRange = $leaseRange
        leaseEventsPerHour = Get-RatePerHour $leaseEvents.Count $leaseRange.hours
        bindingEvents = $bindingEvents.Count
        bindingNonTrue = $bindingNonTrue
        unexplainedBindingNonTrue = $unexplainedBindingNonTrue
        bindingRange = $bindingRange
        bindingEventsPerHour = Get-RatePerHour $bindingEvents.Count $bindingRange.hours
        heartbeatEvents = $heartbeatEvents.Count
        heartbeatNonTrue = $heartbeatNonTrue
        unexplainedHeartbeatNonTrue = $unexplainedHeartbeatNonTrue
        heartbeatRange = $heartbeatRange
        heartbeatEventsPerHour = Get-RatePerHour $heartbeatEvents.Count $heartbeatRange.hours
        leaseSHA256 = (Get-FileHash -LiteralPath $leasePath -Algorithm SHA256).Hash.ToLowerInvariant()
        bindingSHA256 = (Get-FileHash -LiteralPath $bindingPath -Algorithm SHA256).Hash.ToLowerInvariant()
        heartbeatSHA256 = (Get-FileHash -LiteralPath $HeartbeatPath -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    boundedHandoff = $boundedHandoff
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
