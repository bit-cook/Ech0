<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Copyright (C) 2025-2026 lin-snow -->
<template>
  <!-- 模型 -->
  <div class="w-full text-[var(--color-text-secondary)]">
    <!-- 启用 Agent -->
    <div class="flex items-center justify-between mb-4">
      <h2 class="font-semibold">{{ t('agentSetting.enableAgent') }}</h2>
      <BaseSwitch v-model="AgentSetting.enable" :disabled="!editMode" />
    </div>

    <!-- LLM 接口协议 -->
    <div class="flex items-center justify-between mb-4">
      <h2 class="font-semibold">{{ t('agentSetting.protocol') }}</h2>
      <BaseSelect
        v-model="AgentSetting.protocol"
        :options="agentProtocolOptions"
        :disabled="!editMode"
        class="w-40 h-8"
      />
    </div>

    <!-- 模型名称 -->
    <div class="mb-4">
      <h2 class="font-semibold mb-1.5">{{ t('agentSetting.modelName') }}</h2>
      <span v-if="!editMode" class="block truncate opacity-80" v-tooltip="AgentSetting.model">
        {{ AgentSetting.model || t('commonUi.none') }}
      </span>
      <BaseInput
        v-else
        v-model="AgentSetting.model"
        type="text"
        :placeholder="t('agentSetting.modelPlaceholder')"
        class="w-full"
      />
    </div>

    <!-- API Key -->
    <div class="mb-4">
      <h2 class="font-semibold mb-1.5">{{ t('agentSetting.apiKey') }}</h2>
      <span v-if="!editMode" class="block truncate opacity-80">
        {{ AgentSetting.api_key ? '********' : t('commonUi.none') }}
      </span>
      <BaseInput
        v-else
        v-model="AgentSetting.api_key"
        type="password"
        :placeholder="t('agentSetting.apiKeyPlaceholder')"
        class="w-full"
      />
    </div>

    <!-- 自定义 Base URL -->
    <div class="mb-4">
      <h2 class="font-semibold mb-1.5">{{ t('agentSetting.baseUrl') }}</h2>
      <span v-if="!editMode" class="block truncate opacity-80">
        {{ AgentSetting.base_url.length === 0 ? t('commonUi.none') : AgentSetting.base_url }}
      </span>
      <template v-else>
        <BaseInput
          v-model="AgentSetting.base_url"
          :placeholder="t('agentSetting.baseUrlPlaceholder')"
          class="w-full"
        />
      </template>
    </div>

    <!-- 上下文窗口 -->
    <div class="mb-4">
      <h2 class="font-semibold mb-1.5">{{ t('agentSetting.contextWindow') }}</h2>
      <span v-if="!editMode" class="block truncate opacity-80">
        {{ formatTokenSize(AgentSetting.context_window) || t('commonUi.none') }}
      </span>
      <template v-else>
        <BaseInput
          v-model="contextWindowRaw"
          type="text"
          :placeholder="t('agentSetting.contextWindowPlaceholder')"
          class="w-full"
          @input="onContextWindowInput"
          @blur="onContextWindowBlur"
        />
        <p class="text-xs opacity-70 mt-1">{{ t('agentSetting.contextWindowHint') }}</p>
      </template>
    </div>

    <!-- Prompt -->
    <div class="mb-4">
      <h2 class="font-semibold mb-1.5">{{ t('agentSetting.prompt') }}</h2>
      <span v-if="!editMode" class="block truncate opacity-80">
        {{ AgentSetting.prompt.length === 0 ? t('commonUi.none') : AgentSetting.prompt }}
      </span>
      <template v-else>
        <BaseTextArea
          v-model="AgentSetting.prompt"
          :placeholder="t('agentSetting.promptPlaceholder')"
          class="w-full"
          :rows="4"
        />
        <p class="text-xs opacity-70 mt-1">{{ t('agentSetting.promptHint') }}</p>
      </template>
    </div>

    <!-- 多模态支持 -->
    <div class="mb-1">
      <div class="flex items-center justify-between">
        <h2 class="font-semibold">{{ t('agentSetting.multimodal') }}</h2>
        <BaseSwitch v-model="AgentSetting.multimodal" :disabled="!editMode" />
      </div>
      <p class="text-xs opacity-70 mt-1">{{ t('agentSetting.multimodalHint') }}</p>
    </div>

    <!-- 测试连接：卡片底部操作行，右对齐 ghost 按钮（留白分隔，不加分隔线） -->
    <div class="flex justify-end mt-6">
      <BaseButton
        class="px-3 text-sm bg-transparent"
        :loading="agentTesting"
        :title="t('agentSetting.testConnection')"
        @click="handleTestAgentConnection"
      >
        {{ t('agentSetting.testConnection') }}
      </BaseButton>
    </div>
  </div>
</template>

<script setup lang="ts">
import BaseInput from '@/components/common/BaseInput.vue'
import BaseSwitch from '@/components/common/BaseSwitch.vue'
import BaseSelect from '@/components/common/BaseSelect.vue'
import BaseTextArea from '@/components/common/BaseTextArea.vue'
import BaseButton from '@/components/common/BaseButton.vue'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { fetchUpdateAgentSettings, fetchTestAgentConnection } from '@/service/api'
import { theToast } from '@/utils/toast'
import { useSettingStore } from '@/stores'
import { storeToRefs } from 'pinia'
import { AgentProtocol } from '@/enums/enums'
import { parseTokenSize, formatTokenSize } from '@/utils/tokenSize'

defineProps<{ editMode: boolean }>()

const settingStore = useSettingStore()
const { t } = useI18n()
const { getAgentSetting } = settingStore
const { AgentSetting } = storeToRefs(settingStore)

// 上下文窗口以 256k/1m 等友好单位编辑：输入时解析进 store（不回写显示，避免打断输入），
// 失焦时再规整成紧凑的 k/m 形式；store 加载完成（getAgentSetting）后据数值回种输入框。
const contextWindowRaw = ref('')
watch(
  () => AgentSetting.value.context_window,
  (tokens) => {
    contextWindowRaw.value = formatTokenSize(tokens)
  },
  { immediate: true },
)
const onContextWindowInput = () => {
  AgentSetting.value.context_window = parseTokenSize(contextWindowRaw.value)
}
const onContextWindowBlur = () => {
  contextWindowRaw.value = formatTokenSize(AgentSetting.value.context_window)
}

const agentProtocolOptions = computed<{ label: string; value: AgentProtocol }[]>(() => [
  { label: t('agentSetting.protocolOpenAI'), value: AgentProtocol.OPENAI },
  { label: t('agentSetting.protocolOpenAIResponses'), value: AgentProtocol.OPENAI_RESPONSES },
  { label: t('agentSetting.protocolAnthropic'), value: AgentProtocol.ANTHROPIC },
])

// 由父组件的编辑胶囊触发；保存后回填最新设置
const save = async () => {
  await fetchUpdateAgentSettings(settingStore.AgentSetting)
    .then((res) => {
      if (res.code === 1) {
        theToast.success(res.msg)
      }
    })
    .finally(() => {
      getAgentSetting()
    })
}

defineExpose({ save })

// 测试连接：拿当前表单做一次最小探活（不依赖启用开关、不落库）
const agentTesting = ref<boolean>(false)
const handleTestAgentConnection = async () => {
  agentTesting.value = true
  try {
    const res = await fetchTestAgentConnection(settingStore.AgentSetting)
    if (res.code === 1) {
      theToast.success(t('agentSetting.testSuccess'))
    } else {
      theToast.error(t('agentSetting.testFailed', { detail: res.msg }))
    }
  } finally {
    agentTesting.value = false
  }
}

onMounted(() => {
  getAgentSetting()
})
</script>

<style scoped></style>
