import { createHash } from 'node:crypto';
import { releaseAgentChecks } from './real-generation-release-contract.mjs';

// v2.0.4 delta on main. Verified with `git log --first-parent v2.0.3..origin/main`
// on 2026-10-26; #263, #254 and the other open pull requests are excluded.
export const V204_PULL_REQUESTS = [243, 246, 247, 249, 250, 251, 252, 255, 256, 257, 258, 259, 260, 261, 262];
export const V204_INSTRUCTION = '让主力review 没问题就发布吧 有问题就修好';

// Exact, single-release exception. Package flags require future workflow
// checks; they never assert that an unbuilt archive has passed.
export function validateV204Release(receipt, sourceDigest, proofs = {}) {
  const fail = message => { throw new Error('v2.0.4 targeted release: ' + message); };
  const text = value => typeof value === 'string' && value.trim() !== '';
  const evidence = value => Array.isArray(value) && value.length > 0 && value.every(text);
  const bound = value => value?.sourceDigest === sourceDigest && evidence(value.evidence);
  const exact = (value, expected) => Array.isArray(value) && new Set(value).size === value.length
    && JSON.stringify([...value].sort()) === JSON.stringify([...expected].sort());
  const hash = bytes => createHash('sha256').update(bytes).digest('hex');
  if (receipt.version !== 'v2.0.4' || receipt.contractVersion !== 3
    || !/^[a-f0-9]{64}$/.test(sourceDigest) || receipt.sourceDigest !== sourceDigest
    || receipt.baseRelease !== 'v2.0.3' || receipt.baseCommit !== 'e13263c948fbbada3724442fc2c9ee6aaea6cfa2'
    || receipt.includedMain !== '363248539ed7b59a9283e9726b3a650b916adb42'
    || !exact(receipt.includedPullRequests, V204_PULL_REQUESTS)) fail('exact release source and merged scope required');
  const bytes = proofs.authorizationBytes;
  const owner = receipt.ownerException;
  if (!bytes || owner?.authorizationSHA256 !== hash(bytes)) fail('authorization bytes must match');
  const authorization = JSON.parse(bytes);
  if (owner.approvedBy !== 'Ender' || owner.scope !== 'v2.0.4-targeted-no-paid-20261026'
    || owner.instruction !== V204_INSTRUCTION || !bound(owner)
    || authorization.version !== 'v2.0.4' || authorization.approvedBy !== 'Ender'
    || authorization.instruction !== V204_INSTRUCTION || authorization.includedMain !== receipt.includedMain
    || !exact(authorization.includedPullRequests, V204_PULL_REQUESTS)
    || authorization.waiveCompleteGenerationMatrix !== true || authorization.targetedAcceptance !== true
    || authorization.newPaidBudgetCNY !== 0) fail('explicit single-release authorization required');
  const ids = releaseAgentChecks('v2.0.4');
  const paths = ['text-image', 'image-image', 'image-video', 'text-video', 'video-video', 'multi-video'];
  const waived = receipt.deferredAcceptance;
  if (receipt.liveMatrixStatus !== 'not_run_owner_waived' || receipt.agentAcceptanceStatus !== 'not_run_targeted_release'
    || receipt.acceptanceComplete !== false || receipt.releasePublished !== false || receipt.releaseComplete !== false
    || !Array.isArray(receipt.cases) || receipt.cases.length !== 0
    || waived?.status !== 'not_run_owner_waived'
    || !exact(waived.caseKeys, [1, 2].flatMap(round => paths.map(path => round + '/' + path)))
    || !exact(waived.agentCheckIds, ids) || !exact(Object.keys(receipt.agentChecks || {}), ids)
    || Object.values(receipt.agentChecks).some(check => check.status !== 'not_run_targeted_release' || !bound(check))) fail('waived checks must remain unexecuted');
  if (['newPaidBudgetCNY', 'newPaidSubmissions', 'newSpentCNY', 'newPendingCNY'].some(key => receipt[key] !== 0)
    || !Array.isArray(receipt.billingAttempts) || receipt.billingAttempts.length !== 0
    || ['budgetCNY', 'spentCNY', 'pendingCNY'].some(key => receipt[key] !== null)
    || receipt.priorFinancialUncertainty?.status !== 'unresolved'
    || receipt.priorFinancialUncertainty.carriedFromVersion !== 'v2.0.3'
    || receipt.priorFinancialUncertainty.pendingCNY !== null
    || !evidence(receipt.priorFinancialUncertainty.evidence)) fail('no new paid attempts or clearing historical unknown bills');
  if (!proofs.priorReceiptBytes || receipt.priorReceiptSHA256 !== hash(proofs.priorReceiptBytes)
    || receipt.knownLimitations?.carriedFromVersion !== 'v2.0.3'
    || !exact(receipt.knownLimitations.inheritedFailedCheckIds, ['native_video', 'native_audio', 'media_film_review'])
    || !evidence(receipt.knownLimitations.evidence)) fail('retain prior receipt and known media failures');
  // The owner assigned this release's review to the primary agent in the same
  // instruction. Any other non-independent review still fails closed.
  const review = receipt.review;
  const ownerAssignedReview = authorization.ownerAssignedReview === true && authorization.reviewAssignedTo === 'Droid 主力';
  if (review?.result !== 'approved' || !text(review.reviewer) || !bound(review)
    || proofs.reviewedSourceDigest !== sourceDigest
    || (review.independent !== true && !(review.independent === false && ownerAssignedReview && review.ownerAssignedReview === true))
    || (review.ownerAssignedReview === true && review.reviewer !== authorization.reviewAssignedTo)) {
    fail('independent review, or the owner-assigned primary-agent review, must approve the current source');
  }
  for (const id of ['canvasPerformance', 'referenceLimits', 'videoLimits', 'imageResponseFormat', 'assistantAuthorization', 'imageReadback', 'submissionRecovery', 'publisher', 'ci']) {
    if (receipt.verification?.[id]?.status !== 'passed' || !bound(receipt.verification[id])) fail('missing current check: ' + id);
  }
  const local = receipt.verification?.localReleaseGate;
  if (!['passed', 'pending_release_workflow'].includes(local?.status) || local.method !== 'local-script'
    || local.beforeDraft !== true || !bound(local)
    || !local.evidence.includes('.github/workflows/release-desktop.yml')) fail('local gate must run before draft creation');
  if (receipt.upgrade?.status !== 'pending_release_workflow' || receipt.upgrade.preservedData !== null
    || receipt.upgrade.requiredBeforePublish !== true || !bound(receipt.upgrade)) fail('native data preservation remains mandatory and pending');
  const packages = receipt.packages;
  if (!exact(packages?.platforms, ['darwin-arm64', 'darwin-amd64', 'windows-amd64', 'linux-amd64'])
    || packages.status !== 'pending_release_workflow' || packages.archives !== null
    || !evidence(packages.workflowEvidence) || !packages.workflowEvidence.includes('.github/workflows/release-desktop.yml')
    || ['windowsReleasedUpgradeAndRollbackBeforeUpload', 'finalArchiveSmokeBeforeUpload', 'linuxNativeWindowBeforeUpload', 'signedManifestBeforePublish', 'publicReadbackAfterPublish'].some(key => packages[key] !== true)) fail('all four final-package safety checks remain mandatory');
}
