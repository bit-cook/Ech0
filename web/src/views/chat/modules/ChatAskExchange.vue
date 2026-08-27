<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Copyright (C) 2025-2026 lin-snow -->
<template>
  <div class="askdone">
    <div v-for="(row, ri) in rows" :key="ri" class="askdone__row">
      <span class="askdone__mark" aria-hidden="true">✓</span>
      <span class="askdone__question">{{ row.question }}</span>
      <span class="askdone__sep" aria-hidden="true">·</span>
      <span class="askdone__answer">{{ row.answer }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  exchange: App.Api.Chat.ChatAskExchange
}>()

type Row = { question: string; answer: string }

/**
 * A settled round, replayed from what was sent: the question and the words the
 * reader picked or typed. Nothing here is interactive — the decision is made.
 */
const rows = computed<Row[]>(() => {
  const out: Row[] = []
  for (const q of props.exchange.questions) {
    const answer = props.exchange.answers.find((a) => a.question_id === q.id)
    if (!answer) continue
    const picked = answer.selected ?? []
    const text = picked.length > 0 ? picked.join(' · ') : (answer.custom ?? '')
    if (text.length === 0) continue
    out.push({
      question: q.header && q.header.length > 0 ? q.header : q.text,
      answer: text,
    })
  }
  return out
})
</script>

<style scoped>
.askdone {
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
  margin: 0.35rem 0 0.2rem;
  min-width: 0;
  max-width: 100%;
}

.askdone__row {
  display: inline-flex;
  align-items: baseline;
  gap: 0.3rem;
  max-width: 100%;
  min-width: 0;
  font-size: 0.74rem;
  line-height: 1.5;
  color: var(--color-text-muted);
}

.askdone__mark {
  flex-shrink: 0;
  color: var(--color-accent);
  opacity: 0.8;
}

.askdone__question {
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.askdone__sep {
  flex-shrink: 0;
  opacity: 0.5;
}

.askdone__answer {
  flex-shrink: 0;
  max-width: 60%;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  padding: 0 0.35rem;
  border-radius: 999px;
  background: var(--color-accent-soft);
  color: var(--color-text-secondary);
}
</style>
