<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Copyright (C) 2025-2026 lin-snow -->
<template>
  <div class="ask" role="group" :aria-label="t('chatPanel.askGroupLabel')">
    <div class="ask__head">
      <span class="ask__mark" aria-hidden="true">?</span>
      <span v-if="question.header" class="ask__header">{{ question.header }}</span>
      <span v-if="total > 1" class="ask__step">
        {{ t('chatPanel.askProgress', { current: index + 1, total }) }}
      </span>
    </div>

    <p class="ask__text">{{ question.text }}</p>

    <pre v-if="question.detail" class="ask__detail">{{ question.detail }}</pre>

    <div v-if="options.length > 0" class="ask__options">
      <button
        v-for="(opt, oi) in options"
        :key="oi"
        type="button"
        class="ask__option"
        :class="{
          'ask__option--picked': picked.has(opt.label),
          'ask__option--tip': oi === question.recommended,
        }"
        :disabled="pending"
        :aria-pressed="question.multi === true ? picked.has(opt.label) : undefined"
        @click="choose(opt.label)"
      >
        <span class="ask__option-label">{{ opt.label }}</span>
        <span
          v-if="oi === question.recommended"
          class="ask__tip"
          role="img"
          :aria-label="t('chatPanel.askRecommended')"
          :title="t('chatPanel.askRecommended')"
          >★</span
        >
        <span v-if="opt.description" class="ask__option-desc">{{ opt.description }}</span>
      </button>
    </div>

    <div class="ask__foot">
      <button
        v-if="question.multi === true"
        type="button"
        class="ask__submit"
        :disabled="pending || picked.size === 0"
        @click="commitMulti"
      >
        {{ pending ? t('chatPanel.askSending') : t('chatPanel.askConfirm') }}
      </button>
      <button
        v-if="index > 0"
        type="button"
        class="ask__back"
        :disabled="pending"
        @click="emit('back')"
      >
        {{ t('chatPanel.askBack') }}
      </button>
      <span class="ask__hint">{{ t('chatPanel.askTypeHint') }}</span>
    </div>

    <p v-if="error" class="ask__error" role="alert">{{ error }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  ask: App.Api.Chat.ChatAsk
  /** Cursor into `ask.questions`; the round is answered one question at a time. */
  index: number
  /** The answer for the whole round is in flight. */
  pending?: boolean
  error?: string
}>()

const emit = defineEmits<{
  (e: 'answer', answer: App.Api.Chat.ChatAskAnswer): void
  (e: 'back'): void
}>()

const { t } = useI18n()

const total = computed<number>(() => props.ask.questions.length)

const question = computed<App.Api.Chat.ChatAskQuestion>(
  () => props.ask.questions[props.index] ?? props.ask.questions[0],
)

const options = computed<App.Api.Chat.ChatAskOption[]>(() => question.value.options ?? [])

/**
 * Multi-select scratch space. `recommended` never seeds it: a mark the model
 * left is not a choice the reader made.
 */
const picked = ref<Set<string>>(new Set())

watch(
  () => [props.ask.ask_id, props.index] as const,
  () => {
    picked.value = new Set()
  },
)

const choose = (label: string) => {
  if (props.pending === true) return
  if (question.value.multi !== true) {
    emit('answer', { question_id: question.value.id, selected: [label], custom: '' })
    return
  }
  const next = new Set(picked.value)
  if (next.has(label)) next.delete(label)
  else next.add(label)
  picked.value = next
}

const commitMulti = () => {
  if (props.pending === true || picked.value.size === 0) return
  // Ordered by the option list, not by click order: the payload mirrors what the
  // reader saw rather than the path they took through it.
  const selected = options.value.map((o) => o.label).filter((label) => picked.value.has(label))
  emit('answer', { question_id: question.value.id, selected, custom: '' })
}
</script>

<style scoped>
.ask {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
  width: 100%;
  min-width: 0;
  margin: 0.45rem 0 0.2rem;
  padding: 0.6rem 0.75rem;
  border: 1px solid var(--color-border-strong);
  border-radius: 0.75rem;
  background: var(--color-accent-soft);
}

.ask__head {
  display: flex;
  align-items: center;
  gap: 0.35rem;
  min-width: 0;
}

.ask__mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 1rem;
  height: 1rem;
  flex-shrink: 0;
  border: 1px solid var(--color-accent);
  border-radius: 999px;
  color: var(--color-accent);
  font-size: 0.66rem;
  line-height: 1;
}

.ask__header {
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  color: var(--color-text-secondary);
  font-size: 0.78rem;
  line-height: 1.5;
}

.ask__step {
  flex-shrink: 0;
  margin-left: auto;
  color: var(--color-text-muted);
  font-family: var(--font-family-mono);
  font-size: 0.7rem;
  font-variant-numeric: tabular-nums;
  opacity: 0.8;
}

.ask__text {
  margin: 0;
  color: var(--color-text-primary);
  font-size: 0.92rem;
  line-height: 1.6;
}

.ask__detail {
  margin: 0;
  max-height: 9rem;
  overflow: auto;
  overscroll-behavior: contain;
  scrollbar-width: thin;
  padding: 0.4rem 0.5rem;
  border-left: 1px solid var(--color-border-strong);
  color: var(--color-text-secondary);
  font-family: var(--font-family-mono);
  font-size: 0.74rem;
  line-height: 1.6;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.ask__options {
  display: flex;
  flex-wrap: wrap;
  gap: 0.35rem;
  margin-top: 0.1rem;
}

.ask__option {
  display: inline-flex;
  align-items: baseline;
  gap: 0.3rem;
  max-width: 100%;
  min-width: 0;
  padding: 0.25rem 0.6rem;
  border: 1px solid var(--color-border-strong);
  border-radius: 999px;
  background: var(--color-bg-surface);
  color: var(--color-text-secondary);
  font-size: 0.82rem;
  line-height: 1.5;
  text-align: left;
  cursor: pointer;
  transition:
    color 0.18s ease,
    border-color 0.18s ease,
    background 0.18s ease;
}

.ask__option:hover:not(:disabled) {
  border-color: var(--color-accent);
  color: var(--color-accent);
}

.ask__option:disabled {
  cursor: default;
  opacity: 0.6;
}

.ask__option--picked {
  border-color: var(--color-accent);
  color: var(--color-accent);
}

.ask__option--tip {
  border-style: dashed;
}

.ask__option-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.ask__tip {
  flex-shrink: 0;
  color: var(--color-accent);
  font-size: 0.7rem;
  opacity: 0.9;
}

.ask__option-desc {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--color-text-muted);
  font-size: 0.72rem;
}

.ask__foot {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.35rem 0.6rem;
  margin-top: 0.1rem;
}

.ask__submit {
  padding: 0.22rem 0.7rem;
  border: 1px solid var(--color-accent);
  border-radius: 999px;
  background: transparent;
  color: var(--color-accent);
  font-size: 0.8rem;
  line-height: 1.5;
  cursor: pointer;
  transition:
    background 0.18s ease,
    opacity 0.18s ease;
}

.ask__submit:hover:not(:disabled) {
  background: var(--color-accent-soft);
}

.ask__submit:disabled {
  cursor: default;
  opacity: 0.45;
}

.ask__back {
  border: none;
  background: transparent;
  padding: 0;
  color: var(--color-text-secondary);
  font-size: 0.78rem;
  line-height: 1.5;
  cursor: pointer;
  transition: color 0.18s ease;
}

.ask__back:hover:not(:disabled) {
  color: var(--color-accent);
}

.ask__back:disabled {
  cursor: default;
  opacity: 0.5;
}

.ask__hint {
  min-width: 0;
  color: var(--color-text-muted);
  font-size: 0.72rem;
  line-height: 1.5;
}

.ask__error {
  margin: 0;
  color: var(--color-danger);
  font-size: 0.76rem;
  line-height: 1.5;
}
</style>
