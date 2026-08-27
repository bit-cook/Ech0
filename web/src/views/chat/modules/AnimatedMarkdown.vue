<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Copyright (C) 2025-2026 lin-snow -->
<!--
  AnimatedMarkdown —— 逐 token 入场动画 + 实时 markdown 渲染。

  - 复用项目已有的 markdown-it，把 token 流转成 VNode 树（不用 v-html，否则每次重渲染
    都重建 DOM、动画全部重播 → 闪烁）。
  - 块级元素的 key 取源码起始行号（稳定、与内部内容多少无关），文本叶子渲染成持久的
    <DiffText>。流式「只追加」时旧节点的 key 不变 → 不重挂、只有新增内容播放动画。
  - 根节点挂 echo-markdown 类，直接复用项目全套 markdown 排版，保证与结束态一致。
-->
<script lang="ts">
import { defineComponent, h, computed, watch, type VNode, type PropType } from 'vue'
import markdownit, { type Token } from 'markdown-it'
import DiffText from './DiffText.vue'
import { useSmoothReveal } from './useSmoothReveal'
import '@/editor/styles/markdown.scss'

// Token 类型自 markdown-it 15 起由包内声明直接导出，无需再从 parse() 返回值反推。
type Child = VNode | string

const md = markdownit({
  html: false,
  linkify: true,
  typographer: false,
  langPrefix: 'language-',
})

const ANIM_MAP: Record<string, string> = {
  blurIn: 'am-blur-in',
  fadeIn: 'am-fade-in',
}

interface BuildCtx {
  k: () => number
  animate: boolean
  animationClass: string
  duration: number
}

// 文本叶子 → 持久的 DiffText（带稳定 key，实例跨重渲染存活、内部累加）
function textLeaf(text: string, ctx: BuildCtx, key: string): VNode {
  return h(DiffText, {
    key,
    text,
    animationClass: ctx.animationClass,
    duration: ctx.duration,
    animate: ctx.animate,
  })
}

function linkAttrs(token: Token): Record<string, string> {
  const href = String(token.attrGet('href') ?? '')
  const attrs: Record<string, string> = { href }
  if (/^https?:\/\//i.test(href)) {
    attrs.target = '_blank'
    attrs.rel = 'noopener noreferrer'
  }
  return attrs
}

interface Frame {
  tag: string
  attrs: Record<string, unknown>
  children: Child[]
  key: string
}

// 块级元素的 key：取 token 源码起始行号。对「只追加」的流式而言行号稳定，且与块内部
// 内容多少无关——往列表里加项不会改变 <ol>/<ul> 的起始行，于是容器不重挂，只有新项动画。
const blockKey = (token: Token, ctx: BuildCtx): string =>
  token.map ? `b${token.map[0]}` : `bx${ctx.k()}`

// 行内 token 流 → VNode。baseLine = 所属 inline 的起始行，配合行内序号生成稳定 key。
function renderInline(children: Token[], ctx: BuildCtx, baseLine: number): Child[] {
  const out: Child[] = []
  const stack: Frame[] = []
  const sink = (): Child[] => (stack.length ? stack[stack.length - 1].children : out)
  let seq = 0
  const nk = (): string => `${baseLine}:${seq++}` // 行内节点 key：起始行 + 行内序号

  for (const t of children) {
    switch (t.type) {
      case 'text':
        if (t.content) sink().push(textLeaf(t.content, ctx, nk()))
        break
      case 'softbreak':
        sink().push(' ')
        break
      case 'hardbreak':
        sink().push(h('br', { key: nk() }))
        break
      case 'code_inline':
        sink().push(h('code', { key: nk(), class: 'am-code-inline' }, t.content))
        break
      case 'image':
        sink().push(
          h('img', {
            key: nk(),
            src: String(t.attrGet('src') ?? ''),
            alt: t.content,
            loading: 'lazy',
          }),
        )
        break
      case 'link_open':
        stack.push({ tag: 'a', attrs: linkAttrs(t), children: [], key: nk() })
        break
      case 'strong_open':
      case 'em_open':
      case 's_open':
        stack.push({ tag: t.tag, attrs: {}, children: [], key: nk() })
        break
      case 'link_close':
      case 'strong_close':
      case 'em_close':
      case 's_close': {
        const frame = stack.pop()
        if (frame) sink().push(h(frame.tag, { ...frame.attrs, key: frame.key }, frame.children))
        break
      }
      default:
        if (t.content) sink().push(textLeaf(t.content, ctx, nk()))
    }
  }
  // 兜底：流式中途未闭合的行内标记
  while (stack.length) {
    const frame = stack.shift()
    if (frame) out.push(h(frame.tag, { ...frame.attrs, key: frame.key }, frame.children))
  }
  return out
}

function renderCode(token: Token, ctx: BuildCtx): VNode {
  const lang = token.info?.trim().split(/\s+/)[0]
  const codeClass = lang ? `hljs language-${lang}` : 'hljs'
  // 流式中先不高亮（结束态由完整渲染器接手高亮 + 复制/折叠）
  return h('pre', { key: blockKey(token, ctx), class: 'am-pre' }, [
    h('code', { class: codeClass }, token.content),
  ])
}

// 块级 token 流 → VNode
function renderBlocks(tokens: Token[], ctx: BuildCtx): Child[] {
  const out: Child[] = []
  const stack: Frame[] = []
  const sink = (): Child[] => (stack.length ? stack[stack.length - 1].children : out)

  for (const t of tokens) {
    if (t.type === 'inline') {
      const baseLine = t.map ? t.map[0] : ctx.k()
      for (const node of renderInline(t.children ?? [], ctx, baseLine)) sink().push(node)
      continue
    }
    if (t.type === 'fence' || t.type === 'code_block') {
      sink().push(renderCode(t, ctx))
      continue
    }
    if (t.type === 'hr') {
      sink().push(h('hr', { key: blockKey(t, ctx) }))
      continue
    }
    if (t.nesting === 1) {
      // 开标签处定 key（用起始行号），与块内部内容多少无关
      stack.push({ tag: t.tag || 'div', attrs: {}, children: [], key: blockKey(t, ctx) })
    } else if (t.nesting === -1) {
      const frame = stack.pop()
      if (frame) sink().push(h(frame.tag, { ...frame.attrs, key: frame.key }, frame.children))
    } else if (t.content) {
      sink().push(textLeaf(t.content, ctx, blockKey(t, ctx)))
    }
  }
  while (stack.length) {
    const frame = stack.pop()
    if (frame) sink().push(h(frame.tag, { ...frame.attrs, key: frame.key }, frame.children))
  }
  return out
}

// ── 流式增量解析 ──────────────────────────────────────────────
// 流式只追加：最后一个空行之前的顶层块不会再变。冻结这段「已定稿」前缀的解析结果，
// 每拍只重解析未完成的尾部，多段落长答案的解析量从 O(n²) 降到 ~O(n)。
// 关键不变量：仅在「分块解析 ≡ 整文解析」可证明成立时走快路，否则回退整文解析——
// 既不改变任何渲染结果，也不动 blockKey（块从尾部移入前缀时行号不变 → key 不变 → 不重挂）。

// 围栏代码块可包含空行：前缀里 ``` / ~~~ 数量为奇数说明截断点落在未闭合围栏内，须回退。
const fenceBalanced = (s: string): boolean => {
  const m = s.match(/^ {0,3}(`{3,}|~{3,})/gm)
  return m === null || m.length % 2 === 0
}

// 尾部首个非空行若是列表项/引用/缩进续行，可能与前缀末块续接（含松散列表判定），
// 分块解析会与整文不一致 → 回退整文解析。
const tailMayJoinPrefix = (tail: string): boolean => {
  for (const line of tail.split('\n')) {
    if (line.trim().length === 0) continue
    // 列表标记用 (\s|$)：连流式中途的悬空标记（如刚打出的「-」「1.」）也算续接，
    // 否则截断点会落在「- a\n\n-」这种松散列表的半成态里，分块解析与整文不一致。
    return (
      /^ {0,3}([-*+]|\d{1,9}[.)])(\s|$)/.test(line) ||
      /^ {0,3}>/.test(line) ||
      /^(?: {4,}|\t)/.test(line)
    )
  }
  return false
}

const offsetTokenMap = (t: Token, off: number): void => {
  if (t.map) t.map = [t.map[0] + off, t.map[1] + off]
}

// 每个组件实例独立持有前缀缓存（同一时刻只有最后一条 assistant 走动画，隔离仍更稳）。
function createIncrementalParser(): (text: string) => Token[] {
  let cachedPrefix = ''
  let cachedTokens: Token[] = []

  return (text: string): Token[] => {
    const lines = text.split('\n')
    // 末个空行即最后一个顶层块边界；其后是未定稿尾部
    let bi = -1
    for (let i = lines.length - 1; i >= 0; i -= 1) {
      if (lines[i].trim().length === 0) {
        bi = i
        break
      }
    }
    if (bi <= 0) return md.parse(text, {})

    const prefix = lines.slice(0, bi).join('\n') + '\n' // 全局行 0..bi-1
    const tail = lines.slice(bi).join('\n') // 以空行开头，全局行 bi 起
    if (!fenceBalanced(prefix) || tailMayJoinPrefix(tail)) return md.parse(text, {})

    if (prefix !== cachedPrefix) {
      cachedPrefix = prefix
      cachedTokens = md.parse(prefix, {})
    }
    // 尾部独立解析，行号整体偏移 bi → blockKey 与整文解析全局一致
    const tailTokens = md.parse(tail, {})
    for (const t of tailTokens) offsetTokenMap(t, bi)
    return [...cachedTokens, ...tailTokens]
  }
}

export default defineComponent({
  name: 'AnimatedMarkdown',
  props: {
    content: { type: String, default: '' },
    animation: { type: String as PropType<keyof typeof ANIM_MAP>, default: 'blurIn' },
    duration: { type: Number, default: 600 },
    // 是否仍在流式接收（false 时把留住的尾词也揭示出来）
    streaming: { type: Boolean, default: true },
  },
  emits: ['update:revealing'],
  setup(props, { emit }) {
    const reduceMotion =
      typeof window !== 'undefined' &&
      typeof window.matchMedia === 'function' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches

    // 把突发的网络流整成稳定节拍逐词揭示
    const source = computed(() => props.content)
    const streamingRef = computed(() => props.streaming && !reduceMotion)
    const displayed = useSmoothReveal(source, streamingRef)

    // 增量解析器随实例存活，冻结已定稿前缀的解析结果
    const parseStream = createIncrementalParser()

    // 上报「是否仍在揭示」：调用方据此等揭示追平后再切到完整渲染，避免尾巴整坨弹出
    watch(
      [displayed, () => props.content],
      () =>
        emit('update:revealing', !reduceMotion && displayed.value.length < props.content.length),
      { immediate: true },
    )

    return () => {
      const text = reduceMotion ? props.content : displayed.value
      let counter = 0
      const ctx: BuildCtx = {
        k: () => counter++,
        animate: !reduceMotion,
        animationClass: ANIM_MAP[props.animation] ?? ANIM_MAP.fadeIn,
        duration: props.duration,
      }
      const tree = renderBlocks(parseStream(text || ''), ctx)
      return h('div', { class: ['echo-markdown', 'am-root'] }, tree)
    }
  },
})
</script>
