# Resume AgentShoppingList

Updated October19,2026, America/New_York.

## Current live state and immediate action

The human authorized the complete shopping-only rollout and later explicitly authorized server-memory Keycloak admin diagnostics and shopping-only fixes. Beta0.1.0-beta.2/application7970b5c is deployed at https://shopping.vvivid.dev. Public TLS/readiness/discovery pass; unauthenticated MCP401; real container restart preserves integrity/count/totals. No production backups, snapshots, off-server data copies or Mac recovery keys: explicit user policy. Do not repeat deployment, enrollment or secret generation.

**Immediate next action:** finish the user's explicitly requested ChatGPT MCP setup. Signed-in https://chatgpt.com/plugins tab10 has filled Shopping creation form. User-Defined OAuth Client, shopping-chatgpt, client_secret_basic, resourceMCP, five shopping scopes, OIDC onlyopenid. Actualcallbackverified and enabled inKeycloak: https://chatgpt.com/connector_platform_oauth_redirect. Human must privately paste existing secret fromserver using command in docs/CLIENT-SETUP.md and approvecreation/access; requestpending. Then createplugin, completeuserconsent and verifyactualtools. Do notinspectsecretvalue or logit. Phone confirmation remains pending.

**Browser/Codex now verified:** user successfully signed in after submapper fix; protected page navigation after174seconds proves the required provider refresh boundary passed. ActualCodexCLI OAuthsuccess and7liveMCPcalls passed, including onlyonewrite to initialize/select emptyWalmartlist. Preferencescorrect,products/purchasesempty. Actualcontainerrestartwithlist/session retaineddurablecounts/integrity and browserstayssignedin. See latestCHECKPOINTS evidence.

**Rollback timer is disarmed:** gateway/app healthy and enabledbrowser/Codex/routing/persistence/existingSSHMCP checks passed. No timedrollbackpending. Independent directSSHpersonal_prd works; taskcontrolsocket /tmp/shopping-rollout-ssh mayexpire. ChatGPTclientenabledwithverifiedcallback; browsercreation/privatepaste/consent/toolacceptancepending. No private config/binary/database backups exist.

## Read in order

AGENTS.md, CHECKPOINTS.md, REQUIREMENTS.md, SPEC.md, ARCHITECTURE.md, docs/KEYCLOAK.md, docs/DEPLOYMENT-PLAN.md, docs/validation/CP10-no-backup-2026-10-19.md and CP10-subject-mapper-2026-10-19.md, deploy/oauth-compat/VALIDATION.md, docs/CLIENT-SETUP.md. Inspect actualGit/live state; historical notes are not runtime proof.

## Git and application

Main, no remote. Application7970b5c independentlyApproved no-backup correction; bb3eb0d records exact tested image evidence. Current rollout/subjectmapper documentation changes are being committed after freshReviewApproved. Preserve changes/data. CP01/02/04/05/07/08/09 complete locally; CP03/06/10 remain open pending actual browser/client/phone acceptance. Goal not complete.

One Go1.26.9 app serves compact My list/Products/History/Insights, shared owner/scope-checked services and16officialSDKMCPtools. SQLiteWAL/checksummedmigrations/revisions/atomic exactreplays; secureOIDCPKCE/sessionkeyring; checkoff never creates spending; immutable confirmedpurchases drive History/Insights. No automatic migration backups; manual recovery utility unused in production.

Local exactbeta2images both passed actualprocess/browser/SDK/crash/retry/containerstopstart tests and offline pinnedTrivy scans0knownfindings. Server uses testedamd64 image indexsha256:9d268276169da14740fa9a05e2e5765f01eb94c1b4e346aa2129cf8c13c596dd, configefcd5b19be9e3b3ceef3c470bc61c1547769bb9e79278ef74581d6413dbd13f0. Docker accepts index asreference, notconfigdigest; prepared environment corrected. Archive .cache/image-audit/beta2-amd64.tar SHA c4ddcba2bda6693a496a5000484bf0477637fc9383e07a4c29c486d67ccbd2cc. Old05ff303images superseded, neverdeploy.

## Applied server changes

Private /home/admin/agent-shopping-list with compose/.env, releaseartifacts, server-onlysecrets and dedicatedagent-shopping-list-data volume. Oneapp agent-shopping-list-shopping-1, existingproxy network/Traefikimmich-public-websecure/letsencrypt, nohostport. DNS-only shopping A→144.91.70.238 TTL300; Cloudflarerecordf1b09577e5dc816fcf189026c7034cf7. Only existinghumanenrolled with America/New_York/USD/Walmart/Monday. Do not export subject/credentials/tokens or regeneratekeys.

Created reviewed7shopping scopes,2canonicalbearer-only resources,3shoppingclients. Removed inheritedscope assignments. Browserenabled exactcallback https://shopping.vvivid.dev/auth/callback. Codexenabled exactverifiedactualcallback http://127.0.0.1:18444/callback. ChatGPTdisabled/nocallback untilactualuser setupverified. All3nowhavedirectsubjectmapper. Password/service/device/CIBA grantsdisabled; onlyintendedaudiences/scopes, max300s. Background/offlineconsent deferred.

Existing gateway updated to reviewed0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917; configonlyshopping_originadded. Baselinebinary2a22fcc08639e846bf0d49cf90c5ac3ad1a0259c8a4ac97a1e7a50bc442b038a exactlymatchedoldinstalledbinary. Existingissuer200 and freshSSHMCPconnect/run/disconnect pass (job0c65963d3d6bd3b2723c579a04b25b25), usingexistingconnectorgrant; newinteractiveSSHlogin/refreshnotproved. Routingchecks shoppingcorrect200, missing/crossAPI/SSHtarget400, SSHclient→shopping400. No unrelatedserviceschanged.

## Client setup and acceptance remaining

LocalCodex shopping connection added. Initialautomaticloginrequestedissuerdefaultscopes/duplicatedresource; it wasblocked whileclientdisabled and canceled. Correctedonlyshoppingconfig: resource fromdiscoveryonce, callbackport/url18444, clientshopping-codex, explicit5shoppingscopes/noSSH/offline. ActualauthorizationURLverified, butlogin timedoutwithoutcallback; noCodexlogin/tool successclaimed. Re-run explicit-scope login whenhumanready. Do notrestoreexplicitresource duplication. ChatGPTneedsactualcallback/setup/privateusersecretdelivery; nosecretinchat.

Human first usedincorrectcredentials, then completedKeycloakandhitcallbackverificationfailure. Adminexamplediagnosisconfirmedmissingsub; explicitnarrowapprovalclearedautomaticcredential-readrejection. Mapperfixedserver-onlyandfreshsigninpageopened. Do notreuseconsumedcallbackcode/URL. No passwordchanged. Needactualsuccessfulbrowsercallback, sessionrefresh, logout/revocation, clientscope/resourceisolation, Polar/check/nospend/confirmedpurchase/history/insights/retryjourney, actualCodex/ChatGPT andphoneacceptance. Syntheticacceptancedata mustnotmasqueradeaspersonalreceipts.

## Toolchain

Use cachedGo directly: .cache/go-mod/golang.org/toolchain@v0.0.1-go1.26.9.darwin-arm64/bin/go, GOTOOLCHAIN=local, projectGOCACHE/GOMODCACHE, GOPROXY=off. Requiredmakefmt/assets/check/build; opt-inprocess/containeracceptance onlywhenrelevant. Build-translate-review mandatory forcodechanges withhost-runtimeverifiedgpt-6.1-sol/maxBuilder/Review andxhighTranslation. OnReviewDeniedstopforhuman. NoapprebuildneededforthecurrentKeycloakfragmentcorrection.
