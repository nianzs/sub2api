<!--
  Account quality rules (降智运维): scheduled answer probes graded by an
  independent judge model, compared across accounts answering the same
  question, with observe / disable scheduling / remove groups actions.
  Rule cards, operations table and round details are ported from
  ranxi2001/sub2api@f895ea743 (frontend/src/views/admin/AccountQualityView.vue);
  the editor is rewritten for this fork.
-->
<template>
  <AppLayout>
    <div class="quality-workspace">
      <div class="workspace-heading">
        <p class="workspace-subtitle">{{ t('qualityOps.workspaceHint') }}</p>
        <div class="heading-actions">
          <span class="updated-label" aria-live="polite">{{ updatedAt ? t('qualityOps.updatedAt', { time: clock(updatedAt) }) : t('qualityOps.loading') }}</span>
          <button class="btn btn-secondary" :disabled="refreshing" @click="load"><Icon name="refresh" size="sm" :class="{ 'animate-spin': refreshing }" />{{ t('qualityOps.refresh') }}</button>
          <button class="btn btn-primary" data-testid="quality-create-rule" @click="newPlan"><Icon name="plus" size="sm" />{{ t('qualityOps.create') }}</button>
        </div>
      </div>
      <div v-if="error || notice" class="workspace-message" :class="error ? 'message-error' : 'message-success'" :role="error ? 'alert' : 'status'">{{ error || notice }}<button :aria-label="t('qualityOps.dismiss')" @click="error = ''; notice = ''"><Icon name="x" size="sm" /></button></div>
      <div class="workspace-summary">
        <div class="summary-cell"><span class="summary-icon"><Icon name="users" size="md" /></span><div><span>{{ t('qualityOps.rulesTotal') }}</span><strong>{{ rulesLoaded ? plans.length : '—' }}</strong></div></div>
        <div class="summary-cell"><span class="summary-icon is-teal"><Icon name="play" size="md" /></span><div><span>{{ t('qualityOps.rulesActive') }}</span><strong>{{ rulesLoaded ? enabledCount : '—' }}<small v-if="rulesLoaded"> / {{ plans.length }}</small></strong></div></div>
        <div class="summary-cell"><span class="summary-icon is-amber"><Icon name="exclamationCircle" size="md" /></span><div><span>{{ t('qualityOps.latestAttention') }}</span><strong>{{ operationsLoaded ? attentionCount : '—' }}</strong></div><button v-if="attentionCount" class="summary-link" @click="selectedPlanId = null; operationFilter = 'attention'">{{ t('qualityOps.view') }}<Icon name="arrowRight" size="xs" /></button></div>
        <div class="summary-cell"><span class="summary-icon"><Icon name="document" size="md" /></span><div><span>{{ t('qualityOps.loadedRounds') }}</span><strong>{{ operationsLoaded ? operations.length : '—' }}</strong></div></div>
      </div>
      <div class="workspace-columns">
        <section class="workspace-panel rules-panel" aria-labelledby="quality-rules-heading">
          <header class="panel-heading"><div><h3 id="quality-rules-heading">{{ t('qualityOps.ruleLibrary') }}<span class="count-label">{{ plans.length }}</span></h3><p>{{ t('qualityOps.ruleLibraryHint') }}</p></div></header>
          <div class="rule-search"><Icon name="search" size="sm" /><input v-model="search" :aria-label="t('qualityOps.searchRule')" :placeholder="t('qualityOps.searchRule')" /><button v-if="search" :aria-label="t('qualityOps.clearSearch')" @click="search = ''"><Icon name="x" size="sm" /></button></div>
          <button class="all-accounts" :class="{ selected: selectedPlanId === null }" :aria-pressed="selectedPlanId === null" @click="selectedPlanId = null"><Icon name="users" size="sm" />{{ t('qualityOps.allAccounts') }}<span>{{ plans.length }}</span></button>
          <div class="rules-scroll" data-testid="rules-scroll" :aria-busy="rulesLoading">
            <div v-if="rulesError" class="panel-error" role="alert">{{ rulesError }}<button @click="refreshRules">{{ t('qualityOps.retry') }}</button></div>
            <div v-if="!rulesLoaded && rulesLoading" class="skeleton-stack" role="status" :aria-label="t('qualityOps.loading')"><div v-for="n in 4" :key="n" class="rule-skeleton"><span /><span /><span /></div></div>
            <article v-for="plan in filteredPlans" :key="plan.id" class="rule-card" :class="{ selected: selectedPlanId === plan.id }" :data-plan-id="plan.id">
              <div class="rule-card-top"><button class="rule-select" :aria-pressed="selectedPlanId === plan.id" @click="selectedPlanId = plan.id"><span class="account-avatar">{{ name(plan).slice(0, 1) }}</span><span class="min-w-0"><strong :title="name(plan)">{{ name(plan) }}</strong><span class="rule-meta">#{{ plan.account_id }}<span>·</span>{{ t('qualityOps.rule') }} {{ plan.id }}</span></span></button><button class="state-toggle" :class="plan.enabled ? 'state-enabled' : 'state-paused'" :disabled="busy || !!pending[plan.id]" :title="t(plan.enabled ? 'qualityOps.pause' : 'qualityOps.enable')" data-testid="quality-rule-toggle" @click="toggle(plan)"><span />{{ t(plan.enabled ? 'qualityOps.activeShort' : 'qualityOps.paused') }}</button></div>
              <p v-if="plan.pelican_config?.rule_name" class="rule-target" :title="plan.pelican_config.rule_name" data-testid="quality-rule-name">{{ plan.pelican_config.rule_name }}</p>
              <div class="rule-model"><code v-for="model in ruleModels(plan)" :key="model">{{ model }}</code><span v-if="running(plan)" class="running-label">{{ t('qualityOps.running') }}</span></div>
              <div class="rule-target" :title="planGroups(plan)"><Icon name="shield" size="xs" /><span>{{ planGroups(plan) }}</span></div>
              <div class="rule-schedule"><span>{{ t('qualityOps.nextRun') }}</span><time :datetime="plan.next_run_at || undefined">{{ plan.enabled ? date(plan.next_run_at) : '—' }}</time></div>
              <div v-if="!plan.pelican_config?.quality?.judge" class="rule-warning">{{ t('qualityOps.configureJudge') }}</div>
              <footer class="rule-actions"><button @click="history(plan)"><Icon name="document" size="xs" />{{ t('qualityOps.historyShort') }}</button><button :disabled="busy || !!pending[plan.id]" data-testid="quality-rule-edit" @click="edit(plan)">{{ t('qualityOps.editShort') }}</button><button :disabled="!!pending[plan.id] || !plan.enabled || running(plan)" data-testid="quality-rule-run" @click="run(plan)"><Icon name="play" size="xs" />{{ pending[plan.id] === 'run' ? t('qualityOps.submitting') : t('qualityOps.runShort') }}</button></footer>
            </article>
            <div v-if="rulesLoaded && !filteredPlans.length" class="panel-empty"><Icon name="inbox" size="lg" /><p>{{ plans.length ? t('qualityOps.noMatchingRules') : t('qualityOps.empty') }}</p><button v-if="!plans.length" class="btn btn-secondary" @click="newPlan">{{ t('qualityOps.create') }}</button></div>
          </div>
        </section>
        <section class="workspace-panel operations-panel" aria-labelledby="quality-operations-heading">
          <header class="panel-heading"><div><h3 id="quality-operations-heading">{{ t('qualityOps.operations') }}</h3><p>{{ t('qualityOps.operationsSubtitle') }}</p></div><button class="icon-button" :disabled="operationsLoading" :aria-label="t('qualityOps.refreshOperations')" @click="refreshOperations()"><Icon name="refresh" size="sm" :class="{ 'animate-spin': operationsLoading }" /></button></header>
          <div class="operations-toolbar"><div class="scope-label"><span class="scope-dot" />{{ selectedPlan ? name(selectedPlan) : t('qualityOps.allAccounts') }}<button v-if="selectedPlan" :aria-label="t('qualityOps.clearScope')" @click="selectedPlanId = null"><Icon name="x" size="xs" /></button></div><Select v-model="operationFilter" class="operation-filter" size="sm" :options="operationFilterOptions" :aria-label="t('qualityOps.filterRecords')" /></div>
          <div v-if="operationsError" role="alert" class="panel-error">{{ operationsError }}<button @click="refreshOperations()">{{ t('qualityOps.retry') }}</button></div>
          <div class="operations-scroll" data-testid="operations-scroll" :aria-busy="operationsLoading">
            <table class="operations-table"><thead><tr><th>{{ t('qualityOps.time') }}</th><th>{{ t('qualityOps.accounts') }}</th><th>{{ t('qualityOps.testResult') }}</th><th>{{ t('qualityOps.accountAction') }}</th><th><span class="sr-only">{{ t('qualityOps.details') }}</span></th></tr></thead><tbody>
              <template v-if="!operationsLoaded && operationsLoading"><tr v-for="n in 6" :key="`loading-${n}`" class="loading-row"><td v-for="c in 5" :key="c"><span class="cell-skeleton" /></td></tr></template>
              <tr v-for="operation in filteredOperations" :key="operation.id" :class="{ 'selected-row': detailOperation?.id === operation.id && !!historyPlan }" :data-operation-id="operation.id">
                <td class="time-cell" :data-label="t('qualityOps.time')"><strong>{{ clock(operation.started_at) }}</strong><span>{{ day(operation.started_at) }}</span></td>
                <td class="account-cell"><button :title="operation.account_name" @click="selectedPlanId = operation.plan_id"><strong>{{ operation.account_name || `#${operation.account_id}` }}</strong></button><span>{{ t('qualityOps.rule') }} {{ operation.plan_id }}<span class="mx-1">·</span>#{{ operation.account_id }}</span></td>
                <td :data-label="t('qualityOps.testResult')"><span class="test-count" :class="allPassed(operation) ? 'test-passed' : 'test-other'"><Icon :name="allPassed(operation) ? 'checkCircle' : 'exclamationCircle'" size="xs" />{{ operation.passed_count }} / {{ operation.total_count }}</span><span class="cell-secondary">{{ t(operation.status === 'skipped' ? 'qualityOps.skipped' : allPassed(operation) ? 'qualityOps.roundPassed' : 'qualityOps.roundNotPassed') }}<template v-if="operation.skipped_count"> · {{ t('qualityOps.skippedCount', { n: operation.skipped_count }) }}</template></span></td>
                <td class="action-cell" :data-label="t('qualityOps.accountAction')"><button class="outcome-badge" :class="tone(operation.quality_action)" @click="operationDetails(operation)"><span />{{ operationLabel(operation) }}</button><span class="cell-secondary" :title="operationGroups(operation)">{{ operationGroups(operation) }}</span></td>
                <td class="detail-cell"><button class="detail-button" :aria-label="t('qualityOps.openRound', { account: operation.account_name, time: date(operation.started_at) })" @click="operationDetails(operation)"><span>{{ t('qualityOps.details') }}</span><Icon name="arrowRight" size="sm" /></button></td>
              </tr>
            </tbody></table>
            <div v-if="operationsLoaded && !filteredOperations.length" class="panel-empty"><Icon name="document" size="lg" /><h4>{{ t('qualityOps.noResults') }}</h4><p>{{ t('qualityOps.filteredEmptyHint') }}</p></div>
          </div>
          <footer class="operations-footer"><span>{{ t('qualityOps.showingLoaded', { shown: filteredOperations.length, loaded: operations.length }) }}</span><button v-if="cursor" :disabled="moreLoading || operationsLoading" @click="refreshOperations(true)">{{ moreLoading ? t('qualityOps.loading') : t('qualityOps.loadMore') }}<Icon name="arrowDown" size="xs" /></button><span v-else>{{ t('qualityOps.loadedEnd') }}</span></footer>
        </section>
      </div>
    </div>

    <BaseDialog :show="showForm" :title="editingPlan ? t('qualityOps.edit') : t('qualityOps.create')" width="wide" :close-on-escape="!busy && !deleteTarget && !discardPrompt" @close="closeForm">
      <p v-if="formError" role="alert" class="editor-error" data-testid="quality-form-error">{{ formError }}</p>
      <p v-if="groupsError" role="alert" class="editor-error">{{ groupsError }} <button type="button" class="underline" @click="refreshGroups">{{ t('qualityOps.retry') }}</button></p>
      <form id="quality-rule-form" class="quality-editor" @submit.prevent="save"><fieldset :disabled="busy" class="space-y-5">
        <label class="block space-y-1"><span>{{ t('qualityOps.ruleName') }}</span><input v-model="form.rule_name" maxlength="100" class="input" data-testid="quality-rule-name-input" /></label>

        <div v-if="editingPlan" class="editor-account" data-testid="quality-editing-account"><span>{{ t('qualityOps.accounts') }}</span><strong>{{ name(editingPlan) }}</strong><span>#{{ editingPlan.account_id }}</span></div>
        <section v-else class="account-picker" data-testid="quality-account-picker">
          <p class="font-medium">{{ t('qualityOps.accounts') }}</p>
          <div class="grid gap-3 sm:grid-cols-2">
            <div class="space-y-1"><span class="block">{{ t('qualityOps.accountGroup') }}</span><Select v-model="accountGroup" :options="accountGroupOptions" searchable data-testid="quality-account-group" @change="searchAccounts(1)" /></div>
            <label class="space-y-1"><span>{{ t('qualityOps.search') }}</span><span class="flex gap-2"><input v-model="accountSearch" class="input min-w-0" data-testid="quality-account-search" :placeholder="t('qualityOps.search')" @keydown.enter.prevent="searchAccounts(1)" /><button type="button" class="btn btn-secondary shrink-0" @click="searchAccounts(1)"><Icon name="search" size="sm" /></button></span></label>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('qualityOps.accountSelectionHint') }}</p>
          <p v-if="accountsError" role="alert" class="text-sm text-red-600">{{ accountsError }}</p>
          <div class="account-list" :aria-busy="accountsLoading">
            <p v-if="accountsLoading" role="status" class="text-sm text-gray-500 sm:col-span-2">{{ t('qualityOps.loading') }}</p>
            <template v-else>
              <label v-for="account in accounts" :key="account.id" :data-account-id="account.id"><input v-model="selectedAccounts" type="checkbox" :value="account.id" :disabled="!!accountDisabledReason(account.id)" /><span class="min-w-0 break-all">{{ account.name }} <span class="text-gray-500">#{{ account.id }}</span><span v-if="accountDisabledReason(account.id)" class="ml-1 text-xs text-gray-500">{{ accountDisabledReason(account.id) }}</span></span></label>
              <p v-if="!accounts.length" class="text-sm text-gray-500 sm:col-span-2">{{ t('qualityOps.noMatchingAccounts') }}</p>
            </template>
          </div>
          <div class="flex flex-wrap items-center gap-3 text-sm"><button type="button" :aria-label="t('qualityOps.previousAccountPage')" :disabled="accountsLoading || accountPage <= 1" @click="searchAccounts(accountPage - 1)">←</button><span>{{ accountPage }} / {{ accountPages }}</span><button type="button" :aria-label="t('qualityOps.nextAccountPage')" :disabled="accountsLoading || accountPage >= accountPages" @click="searchAccounts(accountPage + 1)">→</button><span aria-live="polite" data-testid="quality-account-count">{{ t('qualityOps.selected', { count: selectedAccounts.length }) }}</span><button v-if="selectedAccounts.length" type="button" class="text-primary-600" @click="selectedAccounts = []">{{ t('qualityOps.clearAccountSelection') }}</button></div>
          <div v-if="selectedAccounts.length" class="selected-accounts"><span v-for="id in selectedAccounts" :key="id">{{ knownAccounts[id] || `#${id}` }}<button type="button" :aria-label="t('qualityOps.removeAccount', { account: knownAccounts[id] || `#${id}` })" @click="selectedAccounts = selectedAccounts.filter(value => value !== id)"><Icon name="x" size="xs" /></button></span></div>
        </section>

        <label class="block space-y-1"><span>{{ t('qualityOps.models') }}</span><input v-model="form.models" required class="input" data-testid="quality-models" placeholder="gpt-5.5, gpt-5.4" /><span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('qualityOps.modelsHint') }}</span></label>
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-1"><span class="block">{{ t('qualityOps.questionKind') }}</span><Select v-model="form.question_kind" :options="questionKindOptions" data-testid="quality-question-kind" /></div>
          <div class="space-y-1"><span class="block">{{ t('qualityOps.effort') }}</span><Select v-model="form.reasoning_effort" :options="effortOptions" data-testid="quality-effort" /></div>
          <div class="space-y-1">
            <span class="flex items-center gap-1">{{ t('qualityOps.cron') }}
              <HelpTooltip>
                <template #trigger><span class="help-dot">?</span></template>
                <div class="space-y-1.5">
                  <p class="font-medium">{{ t('admin.scheduledTests.cronTooltipTitle') }}</p>
                  <p>{{ t('admin.scheduledTests.cronTooltipMeaning') }}</p>
                  <p>{{ t('admin.scheduledTests.cronTooltipExampleEvery30Min') }}</p>
                  <p>{{ t('admin.scheduledTests.cronTooltipExampleHourly') }}</p>
                  <p>{{ t('admin.scheduledTests.cronTooltipExampleDaily') }}</p>
                  <p>{{ t('admin.scheduledTests.cronTooltipExampleWeekly') }}</p>
                  <p>{{ t('admin.scheduledTests.cronTooltipRange') }}</p>
                </div>
              </HelpTooltip>
            </span>
            <Input v-model="form.cron_expression" placeholder="*/30 * * * *" data-testid="quality-cron" :hint="t('admin.scheduledTests.cronHelp')" />
          </div>
          <label class="space-y-1"><span class="block">{{ t('qualityOps.parallel') }}</span><input v-model.number="form.parallel_count" type="number" min="1" :max="QUALITY_MAX_SAMPLES_PER_MODEL" step="1" required class="input" data-testid="quality-parallel" /></label>
        </div>
        <p v-if="form.question_kind === 'random_reasoning'" class="text-sm text-gray-500 dark:text-gray-400" data-testid="quality-random-hint">{{ t('qualityOps.randomHint') }}</p>
        <template v-else>
          <div><div class="mb-2 flex items-center justify-between"><label for="quality-prompt">{{ t('qualityOps.prompt') }}</label><button type="button" class="text-sm text-primary-600" @click="useCandy">{{ t('qualityOps.candy') }}</button></div><textarea id="quality-prompt" v-model="form.prompt" required maxlength="32000" rows="5" class="input text-sm" data-testid="quality-prompt" /></div>
          <label class="block space-y-1"><span>{{ t('qualityOps.answer') }}</span><input v-model="form.expected_answer" required maxlength="4000" class="input" data-testid="quality-expected-answer" /></label>
        </template>

        <fieldset class="editor-section">
          <legend>{{ t('qualityOps.judgeTitle') }}</legend>
          <div class="grid gap-4 sm:grid-cols-2">
            <div class="space-y-1"><span class="block">{{ t('qualityOps.judgeGroup') }}</span><Select v-model="form.judge.group_id" :options="judgeGroupOptions" :placeholder="t('qualityOps.selectJudgeGroup')" searchable data-testid="quality-judge-group" /></div>
            <label class="space-y-1"><span class="block">{{ t('qualityOps.judgeModel') }}</span><input v-model="form.judge.model_id" list="quality-judge-models" required maxlength="100" class="input" data-testid="quality-judge-model" :placeholder="t('qualityOps.selectJudgeModel')" /><datalist id="quality-judge-models"><option v-for="model in judgeModels" :key="model" :value="model" /></datalist></label>
          </div>
          <label class="block space-y-1"><span>{{ t('qualityOps.judgePrompt') }}</span><textarea v-model="form.judge.prompt" required maxlength="16000" rows="3" class="input text-sm" data-testid="quality-judge-prompt" /></label>
          <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('qualityOps.grading') }}</p>
        </fieldset>

        <fieldset class="editor-section">
          <legend>{{ t('qualityOps.failureAction') }}</legend>
          <label class="flex items-center gap-2"><input v-model="form.action" type="radio" value="observe_only" data-testid="quality-action-observe-only" />{{ t('qualityOps.observeOnly') }}</label>
          <label class="flex items-center gap-2"><input v-model="form.action" type="radio" value="disable_scheduling" data-testid="quality-action-disable-scheduling" />{{ t('qualityOps.disableScheduling') }}</label>
          <label class="flex items-center gap-2"><input v-model="form.action" type="radio" value="remove_groups" data-testid="quality-action-remove-groups" />{{ t('qualityOps.removeGroups') }}</label>
          <GroupSelector v-if="qualityUsesGroups(form.action)" v-model="form.remove_group_ids" class="pl-6" :groups="groups" :label="t('qualityOps.targetGroups')" data-testid="quality-target-groups" />
          <label v-if="qualityUsesRetestRest(form.action)" class="block space-y-1" data-testid="quality-retest-rest-setting"><span>{{ t('qualityOps.retestRest') }}</span><input v-model.number="form.retest_rest_minutes" type="number" min="0" max="10080" step="1" required class="input" data-testid="quality-retest-rest" /><span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('qualityOps.retestRestHint') }}</span></label>
          <template v-if="form.action !== 'observe_only'"><label class="flex items-center gap-2"><input v-model="form.auto_restore" type="checkbox" data-testid="quality-auto-restore" />{{ t('qualityOps.autoRestore') }}</label><p class="text-sm text-gray-500 dark:text-gray-400">{{ t('qualityOps.restoreHelp') }}</p></template>
        </fieldset>
        <label class="flex items-center gap-2"><input v-model="form.enabled" type="checkbox" data-testid="quality-rule-enabled" />{{ t('qualityOps.enabled') }}</label>
      </fieldset></form>
      <template #footer><div class="editor-footer"><button v-if="editingPlan" type="button" class="delete-rule" data-testid="quality-editor-delete" :disabled="busy" @click="requestDelete(editingPlan)">{{ t('qualityOps.delete') }}</button><span class="flex-1" /><button type="button" class="btn btn-secondary" :disabled="busy" @click="closeForm">{{ t('qualityOps.cancel') }}</button><button form="quality-rule-form" type="submit" class="btn btn-primary" data-testid="quality-save" :disabled="busy || (!editingPlan && !selectedAccounts.length)">{{ busy ? t('qualityOps.saving') : t('qualityOps.save') }}</button></div></template>
    </BaseDialog>

    <BaseDialog :show="!!historyPlan" :title="detailOperation ? t('qualityOps.roundDetail') : t('qualityOps.history')" width="wide" close-on-click-outside @close="closeDetails">
      <template v-if="historyPlan">
        <div class="detail-heading"><span class="account-avatar">{{ detailAccountName.slice(0, 1) }}</span><div><h3>{{ detailAccountName }}</h3><p>{{ t('qualityOps.rule') }} {{ historyPlan.id }}<span class="mx-2">·</span>{{ detailOperation ? date(detailOperation.started_at) : t('qualityOps.historyHelp') }}</p></div><router-link class="account-management-link" to="/admin/accounts">{{ t('qualityOps.manageAccount') }}<Icon name="externalLink" size="xs" /></router-link></div>
        <div v-if="detailOperation" class="round-overview"><div><span>{{ t('qualityOps.testResult') }}</span><strong :class="allPassed(detailOperation) ? 'text-emerald-600' : 'text-rose-600'">{{ detailOperation.passed_count }} / {{ detailOperation.total_count }} {{ t('qualityOps.passed') }}</strong><span v-if="detailOperation.skipped_count">{{ t('qualityOps.skippedCount', { n: detailOperation.skipped_count }) }}</span></div><div><span>{{ t('qualityOps.accountAction') }}</span><strong>{{ operationLabel(detailOperation) }}</strong></div></div>
        <aside v-if="detailAction === 'restore_conflict'" class="conflict-explanation" role="note"><Icon name="exclamationTriangle" size="md" /><div><h4>{{ t('qualityOps.conflictTitle') }}</h4><p>{{ t('qualityOps.conflictExplanation') }}</p><details class="conflict-causes"><summary>{{ t('qualityOps.conflictChecks') }}</summary><ul><li>{{ t('qualityOps.conflictAccount') }}</li><li>{{ t('qualityOps.conflictMembership') }}</li><li>{{ t('qualityOps.conflictAvailability') }}</li></ul></details><p class="conflict-limit">{{ t('qualityOps.conflictUnknown') }}</p><strong>{{ t('qualityOps.conflictNextStep') }}</strong></div></aside>
        <div v-else-if="detailAction" class="action-explanation"><Icon name="infoCircle" size="sm" /><p>{{ actionExplanation(detailAction) }}</p></div>
        <dl v-if="detailOperation?.pelican_config?.quality" class="detail-policy"><div><dt>{{ t(qualityUsesGroups(detailOperation.pelican_config.quality.action) ? 'qualityOps.targetGroups' : 'qualityOps.ruleAction') }}</dt><dd>{{ operationGroups(detailOperation) }}</dd></div><div><dt>{{ t('qualityOps.autoRestoreShort') }}</dt><dd>{{ t(detailOperation.pelican_config.quality.auto_restore ? 'qualityOps.on' : 'qualityOps.off') }}</dd></div></dl>
        <div v-if="detailsError" class="panel-error" role="alert">{{ detailsError }}<button @click="retryDetails">{{ t('qualityOps.retry') }}</button></div>
        <div v-if="detailsLoading" class="detail-loading" role="status"><span class="cell-skeleton" /><span class="cell-skeleton" />{{ t('qualityOps.loading') }}</div>
        <div v-else class="detail-grid">
          <nav class="result-navigation" :aria-label="t('qualityOps.probes')"><p>{{ t('qualityOps.probes') }}<span>{{ results.length }}</span></p><button v-for="(result, index) in results" :key="result.id" :class="{ selected: selectedResultId === result.id }" :aria-pressed="selectedResultId === result.id" @click="selectResult(result)"><span>{{ detailOperation ? t('qualityOps.probeNumber', { n: index + 1 }) : date(result.started_at) }}</span><code v-if="result.status && result.pelican_config?.model_id" class="break-all text-xs">{{ result.pelican_config.model_id }}</code><span :class="resultTextClass(result)">{{ resultLabel(result) }}</span></button></nav>
          <section class="answer-detail" :aria-busy="answerLoading">
            <div v-if="answerLoading" class="detail-loading" role="status"><span class="cell-skeleton" /><span class="cell-skeleton" />{{ t('qualityOps.loadingAnswer') }}</div>
            <template v-else-if="selectedResult"><header class="answer-heading"><h4>{{ t('qualityOps.answerAndVerdict') }}</h4><span class="outcome-badge" :class="resultTone(selectedResult)" data-testid="quality-result-badge">{{ resultLabel(selectedResult) }}</span></header>
              <details v-if="selectedResult.pelican_config?.random_question" class="answer-reference" data-testid="quality-random-question-detail"><summary>{{ t('qualityOps.generatedQuestion') }} · {{ selectedResult.pelican_config.random_question.id }}</summary><pre class="whitespace-pre-wrap">{{ selectedResult.pelican_config.prompt }}</pre></details>
              <div v-if="selectedResult.pelican_config?.question_kind === 'random_reasoning'" class="answer-reference" data-testid="quality-batch-evidence">
                <template v-if="selectedResult.pelican_config.quality_batch">
                  <strong>{{ t('qualityOps.sharedBatch') }} · {{ selectedResult.pelican_config.quality_batch.id }}</strong>
                  <p>{{ batchDecisionLabel(selectedResult.pelican_config.quality_batch.decision) }}</p>
                  <p>{{ t('qualityOps.batchCondition', { model: selectedResult.pelican_config.model_id || '—', mapped: selectedResult.pelican_config.quality_batch.mapped_model || '—', effort: selectedResult.pelican_config.reasoning_effort || '—', route: selectedResult.pelican_config.quality_batch.route || '—' }) }}</p>
                  <p>{{ t('qualityOps.batchPeers', { total: selectedResult.pelican_config.quality_batch.peer_accounts, passed: selectedResult.pelican_config.quality_batch.peer_passed, failed: selectedResult.pelican_config.quality_batch.peer_failed, unknown: selectedResult.pelican_config.quality_batch.peer_inconclusive }) }}</p>
                  <p v-if="selectedResult.pelican_config.quality_batch.started_at">{{ t('qualityOps.batchWindow', { from: date(selectedResult.pelican_config.quality_batch.started_at), to: date(selectedResult.pelican_config.quality_batch.start_deadline) }) }}</p>
                </template>
                <p v-else>{{ t('qualityOps.batchLegacy') }}</p>
              </div>
              <div v-if="selectedResult.status !== 'skipped'" class="answer-reference"><span>{{ t('qualityOps.answer') }}</span><p>{{ selectedResult.pelican_config?.quality?.expected_answer || detailOperation?.pelican_config?.quality?.expected_answer || '—' }}</p></div>
              <div class="response-heading">{{ t(selectedResult.status === 'skipped' ? 'qualityOps.skipReason' : 'qualityOps.actualAnswer') }}<span v-if="selectedResult.latency_ms">{{ (selectedResult.latency_ms / 1000).toFixed(1) }}s</span></div>
              <pre class="response-content">{{ resultContent(selectedResult) }}</pre>
              <div v-if="selectedResult.quality_judgment" class="judge-reason"><h5>{{ t('qualityOps.judgeReason') }}</h5><p>{{ selectedResult.quality_judgment.reason || '—' }}</p><span>{{ selectedResult.quality_judgment.model_id || '—' }} · {{ groupNames[selectedResult.quality_judgment.group_id || 0] || selectedResult.quality_judgment.group_id || '—' }}</span></div>
            </template><div v-else-if="!detailsError" class="panel-empty">{{ t('qualityOps.noResults') }}</div>
          </section>
        </div>
      </template>
      <template #footer><div class="detail-footer"><button class="btn btn-secondary" :disabled="operationIndex <= 0 || !detailOperation" @click="navigateOperation(-1)"><Icon name="arrowLeft" size="sm" />{{ t('qualityOps.previousRound') }}</button><span>{{ detailOperation ? `${operationIndex + 1} / ${filteredOperations.length}` : t('qualityOps.history') }}</span><button class="btn btn-secondary" :disabled="!detailOperation || operationIndex < 0 || operationIndex >= filteredOperations.length - 1" @click="navigateOperation(1)">{{ t('qualityOps.nextRound') }}<Icon name="arrowRight" size="sm" /></button></div></template>
    </BaseDialog>

    <BaseDialog :show="!!deleteTarget" :title="t('qualityOps.deleteRuleTitle')" width="narrow" :z-index="60" :close-on-escape="!deleting" :show-close-button="!deleting" @close="!deleting && (deleteTarget = null)">
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('qualityOps.deleteConfirm') }}</p>
      <p v-if="deleteTarget" class="mt-3 break-words text-sm font-medium" data-testid="quality-delete-target">{{ name(deleteTarget) }} · {{ t('qualityOps.rule') }} #{{ deleteTarget.id }}</p>
      <p v-if="deleteError" class="editor-error mt-3" role="alert" data-testid="quality-delete-error">{{ deleteError }}</p>
      <template #footer><div class="flex justify-end gap-2"><button class="btn btn-secondary" :disabled="deleting" @click="deleteTarget = null">{{ t('qualityOps.cancel') }}</button><button class="btn bg-red-600 text-white hover:bg-red-700" :disabled="deleting" data-testid="quality-confirm-delete" @click="confirmDelete">{{ deleting ? t('qualityOps.deleting') : t('qualityOps.delete') }}</button></div></template>
    </BaseDialog>
    <BaseDialog :show="discardPrompt" :title="t('qualityOps.unsavedTitle')" width="narrow" :z-index="60" @close="discardPrompt = false"><p>{{ t('qualityOps.unsavedHint') }}</p><template #footer><div class="flex justify-end gap-2"><button class="btn btn-secondary" @click="discardPrompt = false">{{ t('qualityOps.keepEditing') }}</button><button class="btn btn-primary" @click="discardPrompt = false; showForm = false">{{ t('qualityOps.discard') }}</button></div></template></BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import GroupSelector from '@/components/common/GroupSelector.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Input from '@/components/common/Input.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { listQualityOperations, listQualityPlans, runQualityPlan, type QualityOperation } from '@/api/admin/accountQuality'
import scheduledTests from '@/api/admin/scheduledTests'
import { list as listAccounts } from '@/api/admin/accounts'
import { getAllIncludingInactive, getModelAllowlistCandidates } from '@/api/admin/groups'
import { CANDY_ANSWER, CANDY_PROMPT } from '@/utils/intelligenceTest'
import {
  QUALITY_MAX_SAMPLES_PER_MODEL,
  QUALITY_REASONING_EFFORTS,
  defaultQualityRuleForm,
  qualityRuleFormFromPlan,
  qualityRulePayload,
  qualityUsesGroups,
  qualityUsesRetestRest,
  type QualityRuleForm,
  type QualityRulePayload
} from '@/utils/qualityRulePatch'
import type { AccountListItem, AdminGroup, QualityAction, QualityPolicy, ScheduledTestPlan, ScheduledTestResult } from '@/types'

type OperationFilter = 'all' | 'attention' | 'passed' | 'failed'

const { t, te } = useI18n()
const ACCOUNT_PAGE_SIZE = 50

// ---- rules, groups and operations ----
const plans = ref<ScheduledTestPlan[]>([]), groups = ref<AdminGroup[]>([])
const operations = ref<QualityOperation[]>([]), cursor = ref(0)
const rulesLoaded = ref(false), operationsLoaded = ref(false)
const rulesLoading = ref(false), operationsLoading = ref(false), moreLoading = ref(false)
const rulesError = ref(''), operationsError = ref(''), groupsError = ref('')
const updatedAt = ref(0)
const search = ref(''), selectedPlanId = ref<number | null>(null), operationFilter = ref<OperationFilter>('all')
const error = ref(''), notice = ref('')
let ruleVersion = 0, operationVersion = 0, groupVersion = 0
let alive = true, poll: ReturnType<typeof setInterval> | undefined

const groupNames = computed<Record<number, string>>(() => Object.fromEntries(groups.value.map(g => [g.id, g.name])))
const operationFilterOptions = computed(() => [
  { value: 'all', label: t('qualityOps.allOutcomes') },
  { value: 'attention', label: t('qualityOps.needsAttention') },
  { value: 'passed', label: t('qualityOps.passedRounds') },
  { value: 'failed', label: t('qualityOps.otherRounds') }
])
const refreshing = computed(() => rulesLoading.value || operationsLoading.value)
const enabledCount = computed(() => plans.value.filter(p => p.enabled).length)
const attentionActions = new Set(['restore_conflict', 'action_conflict', 'action_error'])
const attentionCount = computed(() => {
  const latest = new Map<number, QualityOperation>()
  for (const operation of operations.value) if (!latest.has(operation.plan_id)) latest.set(operation.plan_id, operation)
  return [...latest.values()].filter(op => attentionActions.has(op.quality_action || '')).length
})
const selectedPlan = computed(() => plans.value.find(p => p.id === selectedPlanId.value))
const filteredPlans = computed(() => {
  const query = search.value.trim().toLowerCase()
  return plans.value.filter(p => !query || `${name(p)} ${p.account_id} ${p.id} ${ruleModels(p).join(' ')} ${p.pelican_config?.rule_name || ''}`.toLowerCase().includes(query))
})
const filteredOperations = computed(() => operations.value.filter(op => {
  if (selectedPlanId.value !== null && op.plan_id !== selectedPlanId.value) return false
  if (operationFilter.value === 'attention') return attentionActions.has(op.quality_action || '')
  if (operationFilter.value === 'passed') return allPassed(op)
  if (operationFilter.value === 'failed') return !allPassed(op)
  return true
}))

function message(e: unknown) {
  const err = e as { response?: { data?: { message?: string; error?: string } }; message?: string }
  return err?.response?.data?.message || err?.response?.data?.error || err?.message || t('qualityOps.error')
}
// Validation helpers throw i18n keys; API errors carry server messages.
function errorText(e: unknown) {
  const key = e instanceof Error ? e.message : ''
  return key && te(key) ? t(key) : message(e)
}

async function refreshRules() {
  const version = ++ruleVersion
  rulesLoading.value = true; rulesError.value = ''
  try {
    const data = await listQualityPlans()
    if (!alive || version !== ruleVersion) return
    plans.value = data; rulesLoaded.value = true; updatedAt.value = Date.now()
    if (selectedPlanId.value !== null && !data.some(p => p.id === selectedPlanId.value)) selectedPlanId.value = null
  } catch (e) { if (alive && version === ruleVersion) rulesError.value = message(e) }
  finally { if (version === ruleVersion) rulesLoading.value = false }
}
async function refreshGroups() {
  const version = ++groupVersion
  groupsError.value = ''
  try {
    const data = await getAllIncludingInactive()
    if (alive && version === groupVersion) groups.value = data
  } catch (e) { if (alive && version === groupVersion) groupsError.value = message(e) }
}
async function refreshOperations(append = false) {
  if (append && (moreLoading.value || operationsLoading.value || !cursor.value)) return
  const version = ++operationVersion
  if (append) moreLoading.value = true
  else { operationsLoading.value = true; moreLoading.value = false }
  operationsError.value = ''
  try {
    const page = await listQualityOperations(append ? cursor.value : 0)
    if (!alive || version !== operationVersion) return
    const unique = new Map<number, QualityOperation>()
    for (const item of [...(append ? operations.value : []), ...page.items]) unique.set(item.id, item)
    operations.value = [...unique.values()]; cursor.value = page.next_cursor
    operationsLoaded.value = true; updatedAt.value = Date.now()
  } catch (e) { if (alive && version === operationVersion) operationsError.value = message(e) }
  finally { if (version === operationVersion) operationsLoading.value = moreLoading.value = false }
}
async function load() { await Promise.all([refreshRules(), refreshGroups(), refreshOperations()]) }

// ---- display helpers ----
function name(plan: Pick<ScheduledTestPlan, 'id' | 'account_id' | 'account_name'>) {
  return plan.account_name || knownAccounts.value[plan.account_id] || `#${plan.account_id ?? plan.id}`
}
function date(value: string | null | undefined) { return value ? new Date(value).toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : '—' }
function clock(value: string | number | undefined) { return value ? new Date(value).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : '—' }
function day(value: string | undefined) { return value ? new Date(value).toLocaleDateString() : '—' }
function running(plan: ScheduledTestPlan) { return !!plan.running_until && Date.parse(plan.running_until) > Date.now() }
function allPassed(op: QualityOperation) { return op.total_count > 0 && op.passed_count === op.total_count }
function ruleModels(rule: Pick<ScheduledTestPlan, 'model_id' | 'pelican_config'>) {
  return rule.pelican_config?.model_ids?.length ? rule.pelican_config.model_ids : [rule.model_id]
}
function policyTarget(quality?: QualityPolicy) {
  if (!quality || quality.action === 'observe_only') return t('qualityOps.observeOnly')
  if (qualityUsesGroups(quality.action)) return quality.remove_group_ids.map(id => groupNames.value[id] || `#${id}`).join(' / ')
  return t('qualityOps.disableSchedulingShort')
}
function planGroups(plan: ScheduledTestPlan) { return policyTarget(plan.pelican_config?.quality) }
function operationGroups(op: QualityOperation) { return policyTarget(op.pelican_config?.quality) }
function actionLabel(action?: string) { const key = `qualityOps.outcomes.${action}`; return action && te(key) ? t(key) : '—' }
function operationLabel(op: QualityOperation) {
  if (op.quality_action === 'inconclusive' && op.pelican_config?.quality_batch?.decision === 'no_passing_peer') return t('qualityOps.batchNoControl')
  if (op.quality_action === 'restored' && op.pelican_config?.quality?.action === 'remove_groups') return t('qualityOps.groupsRestored')
  return actionLabel(op.quality_action)
}
function batchDecisionLabel(decision: string) {
  const key = `qualityOps.batchDecisions.${decision}`
  return te(key) ? t(key) : t('qualityOps.batchLegacy')
}
function tone(action?: string) {
  const key = action || ''
  if (attentionActions.has(key)) return 'tone-warning'
  if (['restored', 'passed'].includes(key)) return 'tone-success'
  if (['groups_removed', 'scheduling_disabled', 'already_quarantined'].includes(key)) return 'tone-muted'
  return 'tone-neutral'
}
function actionExplanation(action: string) {
  const key = `qualityOps.actionHelp.${action}`
  return te(key) ? t(key) : t('qualityOps.actionHelp.no_change')
}
function skipLabel(result: ScheduledTestResult) {
  if (result.error_message === 'model_catalog_unavailable') return 'qualityOps.catalogUnavailable'
  if (result.error_message === 'model_unavailable') return 'qualityOps.modelUnavailableSkipped'
  return 'qualityOps.modelUnsupportedSkipped'
}
function resultLabel(result: ScheduledTestResult) {
  if (result.status === 'skipped') return t(skipLabel(result))
  if (!result.status) return t('qualityOps.notLoaded')
  if (result.status === 'success') return t('qualityOps.passed')
  if (result.error_message === 'answer_mismatch') return t('qualityOps.wrongAnswer')
  if (result.quality_judgment?.verdict === 'unknown' || result.error_message?.startsWith('judge_')) return t('qualityOps.judgeUnknown')
  return t('qualityOps.requestError')
}
function resultTextClass(result: ScheduledTestResult) {
  if (!result.status) return 'text-gray-400'
  if (result.status === 'skipped') return 'text-gray-500 dark:text-gray-400'
  return result.status === 'success' ? 'text-emerald-600' : 'text-rose-600'
}
function resultTone(result: ScheduledTestResult) {
  if (result.status === 'skipped') return 'tone-neutral'
  return result.status === 'success' ? 'tone-success' : 'tone-danger'
}
function resultContent(result: ScheduledTestResult) {
  if (result.status === 'skipped') {
    if (result.error_message === 'model_catalog_unavailable') return t('qualityOps.catalogSkipHelp')
    if (result.error_message === 'model_unavailable') return t('qualityOps.modelUnavailableSkipHelp')
    return t('qualityOps.unsupportedSkipHelp')
  }
  return result.response_text || result.error_message || t('qualityOps.noAnswer')
}

// ---- rule actions ----
const busy = ref(false), pending = ref<Record<number, string>>({})
async function planAction(plan: ScheduledTestPlan, kind: string, fn: () => Promise<unknown>) {
  if (busy.value || pending.value[plan.id]) return
  pending.value[plan.id] = kind; error.value = ''; notice.value = ''
  try {
    await fn()
    if (!alive) return
    if (kind === 'run') notice.value = t('qualityOps.queued')
    await refreshRules()
  } catch (e) { if (alive) error.value = message(e) }
  finally { delete pending.value[plan.id] }
}
async function toggle(plan: ScheduledTestPlan) { await planAction(plan, 'toggle', () => scheduledTests.update(plan.id, { enabled: !plan.enabled })) }
async function run(plan: ScheduledTestPlan) { await planAction(plan, 'run', () => runQualityPlan(plan.id)) }

const deleteTarget = ref<ScheduledTestPlan | null>(null), deleting = ref(false), deleteError = ref('')
function requestDelete(plan: ScheduledTestPlan) {
  if (busy.value || pending.value[plan.id]) return
  deleteTarget.value = plan; deleteError.value = ''
}
async function confirmDelete() {
  const plan = deleteTarget.value
  if (!plan || deleting.value) return
  deleting.value = true; deleteError.value = ''; error.value = ''; notice.value = ''
  try {
    await scheduledTests.delete(plan.id)
    if (!alive) return
    plans.value = plans.value.filter(item => item.id !== plan.id)
    operations.value = operations.value.filter(item => item.plan_id !== plan.id)
    if (selectedPlanId.value === plan.id) selectedPlanId.value = null
    if (historyPlan.value?.id === plan.id) closeDetails()
    if (editingPlan.value?.id === plan.id) showForm.value = false
    deleteTarget.value = null
    notice.value = t('qualityOps.ruleDeleted')
    void refreshRules(); void refreshOperations()
  } catch (e) { if (alive) deleteError.value = message(e) }
  finally { deleting.value = false }
}

// ---- editor ----
const showForm = ref(false), editingPlan = ref<ScheduledTestPlan | null>(null)
const form = ref<QualityRuleForm>(defaultQualityRuleForm(''))
const formError = ref(''), discardPrompt = ref(false), initialForm = ref('')
const selectedAccounts = ref<number[]>([]), knownAccounts = ref<Record<number, string>>({})
const accounts = ref<AccountListItem[]>([]), accountSearch = ref(''), accountGroup = ref('')
const accountPage = ref(1), accountPages = ref(1), accountsLoading = ref(false), accountsError = ref('')
const judgeModels = ref<string[]>([])
let accountRequest = 0, judgeModelsRequest = 0

const questionKindOptions = computed(() => [
  { value: 'random_reasoning', label: t('qualityOps.questionRandom') },
  { value: 'candy', label: t('qualityOps.questionCandy') }
])
const effortOptions = QUALITY_REASONING_EFFORTS.map(effort => ({ value: effort, label: effort }))
const accountGroupOptions = computed(() => [
  { value: '', label: t('qualityOps.allAccountGroups') },
  ...groups.value.map(g => ({ value: String(g.id), label: `${g.name} #${g.id}` }))
])
const judgeGroupOptions = computed(() => groups.value.filter(g => g.status === 'active').map(g => ({ value: g.id, label: `${g.name} #${g.id}` })))
const formSnapshot = () => JSON.stringify([form.value, selectedAccounts.value])

// The backend keeps one observation rule and one quarantine rule per account.
function ruleScope(action?: QualityAction) { return action === 'observe_only' ? 'observation' : 'quarantine' }
const existingAccountIds = computed(() => new Set(plans.value
  .filter(plan => ruleScope(plan.pelican_config?.quality?.action) === ruleScope(form.value.action))
  .map(plan => plan.account_id)))
function accountDisabledReason(id: number) { return existingAccountIds.value.has(id) ? t('qualityOps.accountHasRule') : '' }
watch(() => form.value.action, () => {
  if (!editingPlan.value) selectedAccounts.value = selectedAccounts.value.filter(id => !existingAccountIds.value.has(id))
})

async function searchAccounts(page = 1) {
  const request = ++accountRequest
  accountsLoading.value = true; accountsError.value = ''
  try {
    // Quality probes run through the OpenAI account test path only.
    const data = await listAccounts(page, ACCOUNT_PAGE_SIZE, {
      platform: 'openai',
      search: accountSearch.value.trim() || undefined,
      group: accountGroup.value || undefined,
      lite: 'true',
      sort_by: 'id',
      sort_order: 'asc'
    })
    if (!alive || request !== accountRequest) return
    for (const account of data.items) knownAccounts.value[account.id] = account.name
    accounts.value = data.items; accountPage.value = page
    accountPages.value = Math.max(1, Math.ceil(data.total / ACCOUNT_PAGE_SIZE))
  } catch (e) { if (alive && request === accountRequest) accountsError.value = message(e) }
  finally { if (request === accountRequest) accountsLoading.value = false }
}

async function loadJudgeModels(groupId: number) {
  const request = ++judgeModelsRequest
  judgeModels.value = []
  if (!groupId) return
  try {
    const models = await getModelAllowlistCandidates(groupId)
    if (alive && request === judgeModelsRequest) judgeModels.value = models
  } catch { if (alive && request === judgeModelsRequest) formError.value = t('qualityOps.judgeModelsUnavailable') }
}
watch(() => showForm.value ? form.value.judge.group_id : 0, id => { void loadJudgeModels(id) })

function openForm(plan: ScheduledTestPlan | null) {
  closeDetails()
  editingPlan.value = plan; formError.value = ''; discardPrompt.value = false
  const judgePrompt = t('qualityOps.defaultJudgePrompt')
  form.value = plan ? qualityRuleFormFromPlan(plan, judgePrompt) : defaultQualityRuleForm(judgePrompt)
  selectedAccounts.value = []
  showForm.value = true; initialForm.value = formSnapshot()
  if (!groups.value.length) void refreshGroups()
}
function newPlan() {
  if (busy.value) return
  accountSearch.value = ''; accountGroup.value = ''; accounts.value = []
  openForm(null)
  void searchAccounts(1)
}
function edit(plan: ScheduledTestPlan) {
  if (busy.value || pending.value[plan.id]) return
  openForm(plan)
}
function closeForm() {
  if (busy.value || deleteTarget.value) return
  if (initialForm.value !== formSnapshot()) { discardPrompt.value = true; return }
  showForm.value = false
}
function useCandy() { form.value.prompt = CANDY_PROMPT; form.value.expected_answer = CANDY_ANSWER }

async function save() {
  if (busy.value) return
  formError.value = ''; error.value = ''; notice.value = ''
  let body: QualityRulePayload
  try { body = qualityRulePayload(form.value) } catch (e) { formError.value = errorText(e); return }
  const plan = editingPlan.value
  const targets = plan ? [] : [...selectedAccounts.value]
  if (!plan) {
    if (!targets.length) { formError.value = t('qualityOps.accountRequired'); return }
    if (targets.some(id => existingAccountIds.value.has(id))) { formError.value = t('qualityOps.accountHasRuleConflict'); return }
  }
  busy.value = true
  let changed = false
  try {
    if (plan) {
      await scheduledTests.update(plan.id, body); changed = true
    } else {
      // One rule per account. Created accounts leave the selection so a retry only creates the rest.
      for (const id of targets) {
        await scheduledTests.create({ ...body, account_id: id }); changed = true
        if (!alive) return
        selectedAccounts.value = selectedAccounts.value.filter(value => value !== id)
      }
    }
    if (!alive) return
    showForm.value = false
    notice.value = plan ? t('qualityOps.saved') : t('qualityOps.rulesCreated', { count: targets.length })
    await refreshRules()
  } catch (e) {
    if (!alive) return
    formError.value = message(e)
    if (changed) await refreshRules()
  } finally { busy.value = false }
}

// ---- history and round details ----
const historyPlan = ref<ScheduledTestPlan | null>(null), detailOperation = ref<QualityOperation | null>(null)
const results = ref<ScheduledTestResult[]>([]), selectedResultId = ref<number | null>(null)
const selectedResult = ref<ScheduledTestResult | null>(null)
const detailsLoading = ref(false), answerLoading = ref(false), detailsError = ref('')
const loadedAnswers = new Map<number, ScheduledTestResult>()
let detailRequest = 0, answerRequest = 0
const operationIndex = computed(() => filteredOperations.value.findIndex(op => op.id === detailOperation.value?.id))
const detailAccountName = computed(() => detailOperation.value?.account_name || (historyPlan.value ? name(historyPlan.value) : ''))
const detailAction = computed(() => detailOperation.value?.quality_action || selectedResult.value?.quality_action)

function closeDetails() {
  detailRequest++; answerRequest++
  historyPlan.value = null; detailOperation.value = null; selectedResult.value = null; selectedResultId.value = null
  results.value = []; detailsError.value = ''; detailsLoading.value = answerLoading.value = false; loadedAnswers.clear()
}
async function history(plan: ScheduledTestPlan) {
  closeDetails(); historyPlan.value = plan; detailsLoading.value = true
  const request = ++detailRequest
  try {
    const data = await scheduledTests.listResults(plan.id, 100, false)
    if (!alive || request !== detailRequest) return
    results.value = data
    if (data[0]) await selectResult(data[0])
  } catch (e) { if (alive && request === detailRequest) detailsError.value = message(e) }
  finally { if (request === detailRequest) detailsLoading.value = false }
}
async function operationDetails(operation: QualityOperation) {
  closeDetails(); detailOperation.value = operation
  historyPlan.value = plans.value.find(p => p.id === operation.plan_id) || { id: operation.plan_id, account_id: operation.account_id, account_name: operation.account_name } as ScheduledTestPlan
  results.value = operation.result_ids.map(id => ({ ...operation, id, status: '', error_message: '', response_text: '' }))
  if (results.value[0]) await selectResult(results.value[0])
}
async function selectResult(result: ScheduledTestResult) {
  if (!historyPlan.value) return
  const request = ++answerRequest, planId = historyPlan.value.id
  selectedResultId.value = result.id; selectedResult.value = null; detailsError.value = ''
  const cached = loadedAnswers.get(result.id)
  if (cached) { selectedResult.value = cached; answerLoading.value = false; return }
  answerLoading.value = true
  try {
    const answer = await scheduledTests.getResult(planId, result.id)
    if (!alive || request !== answerRequest) return
    loadedAnswers.set(result.id, answer); selectedResult.value = answer
    results.value = results.value.map(item => item.id === result.id ? answer : item)
  } catch (e) { if (alive && request === answerRequest) detailsError.value = message(e) }
  finally { if (request === answerRequest) answerLoading.value = false }
}
function retryDetails() {
  if (selectedResultId.value !== null) { const result = results.value.find(r => r.id === selectedResultId.value); if (result) void selectResult(result) }
  else if (historyPlan.value) void history(historyPlan.value)
}
function navigateOperation(offset: number) { const next = filteredOperations.value[operationIndex.value + offset]; if (next) void operationDetails(next) }

onMounted(() => {
  void load()
  poll = setInterval(() => {
    if (document.visibilityState === 'visible' && !refreshing.value && !deleting.value) { void refreshRules(); void refreshOperations() }
  }, 30_000)
})
onBeforeUnmount(() => {
  alive = false; accountRequest++; judgeModelsRequest++; detailRequest++; answerRequest++
  if (poll) clearInterval(poll)
  loadedAnswers.clear()
})
</script>

<style scoped>
.quality-workspace { height: calc(100dvh - 8rem - 1px); min-height: 34rem; @apply flex w-full min-w-0 flex-col gap-4 text-gray-900 dark:text-gray-100; }
.quality-workspace > :not(.workspace-columns) { flex-shrink: 0; }
.workspace-heading { @apply flex flex-wrap items-center justify-between gap-4; }
.workspace-subtitle { @apply text-sm text-gray-500 dark:text-gray-400; }
.heading-actions { @apply flex flex-wrap items-center gap-2; }
.heading-actions .btn, .detail-footer .btn { @apply inline-flex items-center gap-2; }
.updated-label { @apply mr-2 hidden text-xs tabular-nums text-gray-400 xl:block; }
.workspace-message { @apply flex items-center justify-between gap-4 rounded-xl border px-4 py-3 text-sm; }
.message-error, .editor-error { @apply border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300; }
.message-success { @apply border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-300; }
.workspace-summary { @apply grid grid-cols-2 rounded-2xl border border-gray-200/80 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900 lg:grid-cols-4; }
.summary-cell { @apply relative flex items-center gap-3 px-5 py-4; }
.summary-cell + .summary-cell { @apply lg:border-l lg:border-gray-100 lg:dark:border-dark-700; }
.summary-icon { @apply flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-slate-100 text-slate-500 dark:bg-dark-800 dark:text-gray-400; }
.summary-icon.is-teal { @apply bg-emerald-50 text-emerald-600 dark:bg-emerald-950/40; }
.summary-icon.is-amber { @apply bg-amber-50 text-amber-600 dark:bg-amber-950/40; }
.summary-cell div > span { @apply block text-xs text-gray-500 dark:text-gray-400; }
.summary-cell strong { @apply mt-1 block text-2xl font-semibold tabular-nums tracking-tight; }
.summary-cell strong small { @apply text-sm font-normal text-gray-400; }
.summary-link { @apply ml-auto inline-flex items-center gap-1 text-xs text-primary-600; }
.workspace-columns { display: grid; grid-template-columns: clamp(320px, 30%, 480px) minmax(0, 1fr); gap: 16px; flex: 1; min-height: 0; }
.workspace-panel { @apply flex min-h-0 min-w-0 flex-col overflow-hidden rounded-2xl border border-gray-200/80 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900; }
.panel-heading { @apply flex shrink-0 items-center justify-between gap-3 px-5 pb-4 pt-5; }
.panel-heading h3 { @apply flex items-center gap-2 text-base font-semibold; }
.panel-heading p { @apply mt-1 text-xs leading-relaxed text-gray-500 dark:text-gray-400; }
.count-label { @apply rounded-md bg-gray-100 px-1.5 py-0.5 text-xs tabular-nums text-gray-500 dark:bg-dark-800; }
.icon-button { @apply flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border border-gray-200 text-gray-500 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-800; }
.rule-search { @apply mx-4 mb-3 flex shrink-0 items-center gap-2 rounded-lg border border-gray-200 bg-gray-50 px-3 text-gray-400 dark:border-dark-700 dark:bg-dark-800; }
.rule-search input { @apply h-9 min-w-0 flex-1 bg-transparent text-sm text-gray-800 outline-none dark:text-gray-100; }
.all-accounts { @apply mx-4 mb-3 flex shrink-0 items-center gap-2 rounded-lg px-3 py-2.5 text-left text-sm text-gray-500 dark:text-gray-400; }
.all-accounts span:last-child { @apply ml-auto text-xs tabular-nums; }
.all-accounts.selected { @apply bg-primary-50 font-medium text-primary-700 dark:bg-primary-950/40 dark:text-primary-300; }
.rules-scroll { @apply min-h-0 flex-1 space-y-3 overflow-y-auto overscroll-contain px-4 pb-4; scrollbar-gutter: stable; }
.rule-card { @apply rounded-xl border border-gray-200 p-3.5 transition-colors dark:border-dark-700; }
.rule-card.selected { @apply border-primary-300 bg-primary-50/30 ring-1 ring-primary-100 dark:border-primary-700 dark:bg-primary-950/20 dark:ring-primary-900; }
.rule-card-top { @apply flex items-start gap-2; }
.rule-select { @apply flex min-w-0 flex-1 items-start gap-2.5 text-left; }
.account-avatar { @apply flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-slate-100 text-xs font-semibold text-slate-600 dark:bg-dark-700 dark:text-gray-300; }
.rule-select strong { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; @apply text-[13px] font-semibold leading-5; }
.rule-meta { @apply mt-1 flex gap-1.5 text-[11px] tabular-nums text-gray-400; }
.state-toggle { @apply inline-flex shrink-0 items-center gap-1 whitespace-nowrap rounded-full px-2 py-1 text-[10px] font-medium; }
.state-toggle > span, .outcome-badge > span { @apply h-1.5 w-1.5 rounded-full bg-current; }
.state-enabled { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400; }
.state-paused { @apply bg-gray-100 text-gray-500 dark:bg-dark-800; }
.rule-model { @apply mt-3 flex flex-wrap items-center gap-2 text-[11px] text-gray-600 dark:text-gray-400; }
.rule-model code { overflow-wrap: anywhere; @apply min-w-0 rounded bg-gray-100 px-1.5 py-0.5 dark:bg-dark-800; }
.running-label { @apply text-primary-600; }
.rule-target { @apply mt-2 flex min-w-0 items-center gap-1.5 text-[11px] text-gray-500; }
.rule-target span { @apply truncate; }
.rule-schedule { @apply mt-3 flex justify-between gap-2 text-[11px] tabular-nums text-gray-400; }
.rule-schedule time { @apply text-gray-600 dark:text-gray-300; }
.rule-warning { @apply mt-2 text-xs text-amber-600; }
.rule-actions { @apply mt-3 flex flex-wrap items-center gap-1 border-t border-gray-100 pt-2 dark:border-dark-700; }
.rule-actions button { @apply inline-flex min-h-8 flex-1 items-center justify-center gap-1 whitespace-nowrap rounded-md text-[11px] text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:hover:bg-dark-800 dark:hover:text-gray-100; }
.rule-actions button:first-child { @apply text-primary-600; }
.operations-toolbar { @apply flex shrink-0 flex-wrap items-center justify-between gap-2 border-b border-t border-gray-100 bg-gray-50/60 px-5 py-3 dark:border-dark-700 dark:bg-dark-800/50; }
.scope-label { overflow-wrap: anywhere; @apply flex min-w-0 items-center gap-2 text-xs font-medium text-gray-600 dark:text-gray-300; max-width: 65%; }
.scope-dot { @apply h-1.5 w-1.5 shrink-0 rounded-full bg-primary-500; }
.operation-filter { @apply w-44 max-w-full; }
.operations-scroll { @apply min-h-0 flex-1 overflow-auto overscroll-contain; scrollbar-gutter: stable; }
.operations-table { @apply w-full text-left text-xs; min-width: 620px; }
.operations-table thead { @apply sticky top-0 z-10 bg-white text-gray-400 dark:bg-dark-900; box-shadow: 0 1px 0 rgb(148 163 184 / .15); }
.operations-table th { @apply whitespace-nowrap px-4 py-3 font-medium; }
.operations-table td { @apply border-b border-gray-100 px-4 py-4 align-middle dark:border-dark-800; }
.operations-table tbody tr:hover, .selected-row { @apply bg-slate-50/80 dark:bg-dark-800/70; }
.time-cell { @apply whitespace-nowrap tabular-nums; }
.time-cell strong { @apply block text-xs font-medium; }
.time-cell > span, .account-cell > span { @apply mt-1 block text-[10px] text-gray-400; }
.account-cell { max-width: 220px; }
.account-cell button { @apply max-w-full text-left hover:text-primary-600; }
.account-cell strong { @apply block truncate font-medium; max-width: 190px; }
.test-count { @apply inline-flex items-center gap-1.5 whitespace-nowrap font-medium tabular-nums; }
.test-passed { @apply text-emerald-600 dark:text-emerald-400; }
.test-other { @apply text-rose-500 dark:text-rose-400; }
.cell-secondary { @apply mt-1.5 block truncate text-[10px] text-gray-400; max-width: 170px; }
.outcome-badge { @apply inline-flex items-center gap-1.5 whitespace-nowrap rounded-md px-2 py-1 text-[11px] font-medium; }
.tone-success { @apply bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300; }
.tone-warning { @apply bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300; }
.tone-muted { @apply bg-slate-100 text-slate-600 dark:bg-dark-700 dark:text-gray-300; }
.tone-neutral { @apply bg-gray-100 text-gray-500 dark:bg-dark-800; }
.tone-danger { @apply bg-rose-50 text-rose-600 dark:bg-rose-950/40 dark:text-rose-300; }
.detail-button { @apply inline-flex min-h-8 items-center gap-1 whitespace-nowrap rounded-md px-1 text-xs text-gray-400 hover:text-primary-600; }
.operations-footer { @apply flex min-h-14 shrink-0 items-center justify-between gap-3 border-t border-gray-100 px-5 text-[11px] text-gray-400 dark:border-dark-700; }
.operations-footer button { @apply inline-flex items-center gap-1.5 text-primary-600; }
.panel-error { @apply m-3 flex items-center justify-between gap-3 rounded-lg bg-red-50 p-3 text-xs text-red-700 dark:bg-red-950/40 dark:text-red-300; }
.panel-error button { @apply shrink-0 underline; }
.panel-empty { @apply flex min-h-52 flex-col items-center justify-center gap-3 p-6 text-center text-sm text-gray-400; }
.panel-empty p { @apply max-w-sm text-xs leading-relaxed; }
.rule-skeleton { @apply space-y-3 rounded-xl border border-gray-100 p-5 dark:border-dark-700; }
.cell-skeleton, .rule-skeleton span { @apply block h-3 rounded bg-gray-100 dark:bg-dark-700; animation: quality-pulse 1.7s ease-in-out infinite; }
.rule-skeleton span:first-child { @apply h-5 w-3/4; }
.rule-skeleton span:last-child { @apply w-1/2; }
.skeleton-stack { @apply space-y-3; }
.detail-heading { @apply mb-5 flex items-center gap-3; }
.detail-heading h3 { @apply text-base font-semibold; }
.detail-heading p { @apply mt-1 text-xs text-gray-400; }
.account-management-link { @apply ml-auto inline-flex shrink-0 items-center gap-1 text-xs text-primary-600; }
.round-overview { @apply mb-5 grid grid-cols-2 divide-x divide-gray-200 rounded-xl border border-gray-200 bg-gray-50 dark:divide-dark-700 dark:border-dark-700 dark:bg-dark-800; }
.round-overview > div { @apply p-4; }
.round-overview span { @apply block text-xs text-gray-500; }
.round-overview strong { @apply mt-2 block text-sm font-semibold; }
.conflict-explanation { @apply mb-5 flex items-start gap-3 rounded-xl border border-amber-200 bg-amber-50/80 p-4 text-amber-900 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200; }
.conflict-explanation > svg { @apply mt-0.5 shrink-0; }
.conflict-explanation h4 { @apply mb-2 text-sm font-semibold; }
.conflict-explanation p, .conflict-explanation li, .conflict-explanation strong { @apply text-xs leading-6; }
.conflict-causes summary { @apply cursor-pointer text-xs font-medium leading-7; }
.conflict-explanation ul { @apply my-2 list-disc pl-4; }
.conflict-limit { @apply mb-2 text-amber-700 dark:text-amber-400; }
.action-explanation { @apply mb-4 flex gap-2 rounded-lg bg-gray-50 p-3 text-xs leading-relaxed text-gray-500 dark:bg-dark-800 dark:text-gray-400; }
.action-explanation svg { @apply shrink-0; }
.detail-policy { @apply mb-5 space-y-2 text-xs; }
.detail-policy > div { @apply flex justify-between gap-4; }
.detail-policy dt { @apply shrink-0 text-gray-400; }
.detail-policy dd { @apply min-w-0 break-words text-right text-gray-600 dark:text-gray-300; }
.detail-grid { display: grid; grid-template-columns: 145px minmax(0, 1fr); @apply gap-4; }
.result-navigation { @apply max-h-[55dvh] space-y-2 overflow-y-auto; }
.result-navigation > p { @apply mb-3 flex justify-between text-xs text-gray-400; }
.result-navigation button { @apply block w-full rounded-lg border border-transparent p-2.5 text-left text-xs hover:bg-gray-50 dark:hover:bg-dark-800; }
.result-navigation button.selected { @apply border-primary-200 bg-primary-50 dark:border-primary-800 dark:bg-primary-950/30; }
.result-navigation button span { @apply block; }
.result-navigation button span + span { @apply mt-1.5 text-[10px]; }
.answer-detail { @apply min-w-0 space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-700; }
.answer-heading { @apply mb-4 flex items-center justify-between gap-2; }
.answer-heading h4 { @apply text-sm font-semibold; }
.answer-reference { @apply rounded-lg bg-emerald-50/60 p-3 dark:bg-emerald-950/20; }
.answer-reference span { @apply text-[11px] text-emerald-700 dark:text-emerald-400; }
.answer-reference p { @apply mt-1 whitespace-pre-wrap break-words text-sm; }
.response-heading { @apply mb-2 mt-4 flex justify-between text-xs text-gray-500; }
.response-content { @apply max-h-[40dvh] overflow-auto whitespace-pre-wrap break-words rounded-lg bg-gray-50 p-4 font-sans text-sm leading-7 text-gray-800 dark:bg-dark-800 dark:text-gray-200; }
.judge-reason { @apply mt-4 border-t border-gray-100 pt-4 dark:border-dark-700; }
.judge-reason h5 { @apply text-xs font-semibold; }
.judge-reason p { @apply mt-2 whitespace-pre-wrap break-words text-sm leading-6 text-gray-600 dark:text-gray-300; }
.judge-reason > span { @apply mt-3 block text-[10px] text-gray-400; }
.detail-loading { @apply space-y-4 py-8 text-xs text-gray-400; }
.detail-footer { @apply flex w-full items-center justify-between gap-2 text-xs text-gray-400; }
.editor-footer { @apply flex w-full items-center gap-2; }
.editor-error { @apply mb-4 rounded-lg border p-3 text-sm; }
.delete-rule { @apply text-sm text-red-600; }
.quality-editor label, .quality-editor legend, .quality-editor .space-y-1 > span { @apply text-sm; }
.quality-editor .input { @apply w-full; }
.editor-section { @apply space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600; }
.editor-section legend { @apply px-2 font-medium; }
.editor-account { @apply flex flex-wrap items-baseline gap-2 rounded-lg bg-gray-50 px-3 py-2 text-sm dark:bg-dark-800; }
.editor-account span:last-child { @apply text-xs text-gray-500; }
.account-picker { @apply space-y-3; }
.account-list { @apply grid max-h-52 gap-2 overflow-auto rounded-lg border border-gray-200 p-3 dark:border-dark-600 sm:grid-cols-2; }
.account-list label { @apply flex items-center gap-2 text-sm; }
.selected-accounts { @apply flex flex-wrap gap-2; }
.selected-accounts > span { @apply inline-flex items-center gap-1 rounded-md bg-primary-50 px-2 py-1 text-xs text-primary-700 dark:bg-primary-950/40 dark:text-primary-300; }
.help-dot { @apply inline-flex h-4 w-4 cursor-help items-center justify-center rounded-full border border-gray-400/70 text-[10px] font-semibold text-gray-400 transition-colors hover:border-primary-500 hover:text-primary-600 dark:border-gray-500 dark:text-gray-500; }
button:disabled { @apply cursor-not-allowed opacity-40; }
button:focus-visible, input:focus-visible, select:focus-visible, a:focus-visible { @apply outline-none ring-2 ring-primary-400 ring-offset-2 dark:ring-offset-dark-900; }
@keyframes quality-pulse { 50% { opacity: .45; } }
@media (min-width: 1280px) and (max-height: 900px) {
  .quality-workspace { gap: 12px; }
  .workspace-subtitle { display: none; }
  .summary-cell { padding-top: 10px; padding-bottom: 10px; }
  .summary-cell strong { font-size: 20px; }
  .panel-heading { padding-top: 12px; padding-bottom: 12px; }
  .panel-heading p { display: none; }
  .rule-card { padding: 12px; }
  .rule-model, .rule-schedule, .rule-actions { margin-top: 8px; }
}
@media (max-width: 1279px) {
  .quality-workspace { height: auto; min-height: 0; }
  .workspace-columns { grid-template-columns: minmax(0, 1fr); }
  .rules-panel { height: clamp(24rem, 60dvh, 42rem); }
  .operations-panel { height: clamp(28rem, 70dvh, 52rem); }
}
@media (max-width: 640px) {
  .operations-table { display: block; min-width: 0; }
  .operations-table thead { @apply sr-only; }
  .operations-table tbody { display: block; }
  .operations-table tbody tr { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; padding: 16px; @apply border-b border-gray-100 dark:border-dark-800; }
  .operations-table td { min-width: 0; max-width: none; padding: 0; border: 0; }
  .operations-table td::before { content: attr(data-label); @apply mb-1.5 block text-[10px] text-gray-400; }
  .operations-table .account-cell { grid-column: 1 / -1; grid-row: 1; }
  .operations-table .account-cell strong { max-width: none; white-space: normal; overflow-wrap: anywhere; }
  .operations-table .action-cell { grid-column: 1 / -1; }
  .operations-table .detail-cell { grid-column: 1 / -1; text-align: right; }
  .operations-table .outcome-badge { white-space: normal; text-align: left; }
  .operations-table .cell-secondary { max-width: 100%; }
  .operations-footer { @apply flex-wrap gap-1 px-4 py-3; }
  .scope-label { max-width: 100%; }
  .editor-footer { flex-wrap: wrap; }
  .summary-cell { padding: 14px 12px; gap: 8px; }
  .summary-cell strong { font-size: 22px; }
  .summary-icon { width: 32px; height: 32px; }
  .summary-link { display: none; }
  .heading-actions { width: 100%; justify-content: flex-end; }
  .detail-grid { grid-template-columns: 1fr; }
  .result-navigation { display: flex; gap: 8px; overflow-x: auto; }
  .result-navigation > p { display: none; }
  .result-navigation button { min-width: 100px; }
  .account-management-link { font-size: 10px; }
  .detail-footer .btn { font-size: 11px; padding: 8px; }
}
@media (prefers-reduced-motion: reduce) { .cell-skeleton, .rule-skeleton span { animation: none; } }
</style>
