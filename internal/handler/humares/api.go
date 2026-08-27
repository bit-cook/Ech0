// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package humares

import (
	"reflect"
	"strconv"
	"strings"
	"unicode"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humagin"
	"github.com/gin-gonic/gin"
)

// SecuritySchemeBearer 是 OpenAPI 安全方案名，operation 用它声明所需 scope（见 Secured）。
const SecuritySchemeBearer = "bearerAuth"

// schemaRefPrefix 与 huma.DefaultConfig 使用的前缀保持一致。
const schemaRefPrefix = "#/components/schemas/"

// newSchemaNamer 返回一个**有状态、撞名消歧**的 schema 命名器。
//
// huma 的 DefaultSchemaNamer 忽略包名（comment.SystemSetting 与 setting.SystemSetting
// 都叫 SystemSetting），跨包同名会在注册时 panic（文档要求此时自定义命名器）。这里：
// 默认沿用简洁名；仅当某简洁名已被**另一个类型**占用时，给后来者加包名段前缀消歧，
// 必要时再加序号兜底。绝大多数 schema 名保持干净，且对未来的同名冲突鲁棒。
func newSchemaNamer() func(reflect.Type, string) string {
	used := map[string]reflect.Type{}
	return func(t reflect.Type, hint string) string {
		name := huma.DefaultSchemaNamer(t, hint)
		key := derefType(t)
		if prev, ok := used[name]; ok && prev != key {
			qualified := packageQualifiedName(t, hint)
			name = qualified
			for i := 2; ; i++ {
				if p, ok := used[name]; !ok || p == key {
					break
				}
				name = qualified + strconv.Itoa(i)
			}
		}
		used[name] = key
		return name
	}
}

// packageQualifiedName 复刻 DefaultSchemaNamer 的拆分，但保留每个点分部件的「包名段」前缀
// （EchoEcho 这类包名与类型名重复时折叠为 Echo），从而对跨包同名消歧。
func packageQualifiedName(t reflect.Type, hint string) string {
	name := derefType(t).String()
	if name == "" || name == "interface {}" {
		name = hint
	}
	name = strings.ReplaceAll(name, "[]", "List[")
	var b strings.Builder
	for _, part := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '[' || r == ']' || r == '*' || r == ','
	}) {
		typeName, pkgSeg := part, ""
		if pkgPath, tn, ok := strings.CutLast(part, "."); ok {
			typeName, pkgSeg = tn, pkgPath
			if _, seg, ok := strings.CutLast(pkgPath, "/"); ok {
				pkgSeg = seg
			}
		}
		seg, tn := titleCase(pkgSeg), titleCase(typeName)
		if seg != "" && !strings.EqualFold(seg, tn) {
			b.WriteString(seg)
		}
		b.WriteString(tn)
	}
	return b.String()
}

func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func titleCase(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// NewAPI 在给定的 gin engine + 无组级鉴权的 group 上创建统一的 Huma API 实例。
// auth/scope 全部下沉为 per-operation 中间件（见 router 层 + Bridge），
// 这样 public / auth / optional 三种姿态能共存于同一份 OpenAPI 文档。
//
// basePath 写入 OpenAPI.Servers，使 spec 中的相对路径（如 /echo/query）拼上 /api 前缀。
// docs 在 group 前缀下：/api/docs、/api/openapi.json、/api/openapi.yaml。
//
// docs 选择文档面板：默认 DocsRendererStoplight 直接用 Huma 内置面板；DocsRendererScalar 时
// 关掉内置 docs、改挂离线自托管的 Scalar（见 docs.go）。两者都不影响生成的 OpenAPI spec。
func NewAPI(engine *gin.Engine, group *gin.RouterGroup, title, version, basePath string, docs DocsRenderer) huma.API {
	installErrorModel()

	cfg := huma.DefaultConfig(title, version)
	// 对齐 gin ShouldBind 的宽松行为（NewAPI 会把这些配置拷进下方自定义 registry）：
	//   - 字段默认 optional：前端到处发部分 body / 省略可选查询参数，Huma 默认却全当 required，
	//     会把这些请求全 422。显式 `required:"true"` 仍必填，path 参数始终必填。
	//   - 允许额外属性：前端常把 GET 来的完整 model（字段比对应 *Dto 多）整个 PUT 回去，
	//     Huma 默认 additionalProperties:false 会因「unexpected property」422；gin 则忽略未知字段。
	cfg.FieldsOptionalByDefault = true
	cfg.AllowAdditionalPropertiesByDefault = true
	// 用撞名消歧的命名器替换默认 registry（跨包同名类型如 SystemSetting 否则会 panic）。
	cfg.Components.Schemas = huma.NewMapRegistry(schemaRefPrefix, newSchemaNamer())
	cfg.Servers = []*huma.Server{{URL: basePath}}
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		SecuritySchemeBearer: {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
	}

	// 关掉默认的 schema-link transformer：它会往响应体注入 $schema 字段、加 Link 头，
	// 破坏「信封不变」。清空 CreateHooks/Transformers 并停掉 /schemas 路由。
	cfg.CreateHooks = nil
	cfg.Transformers = nil
	cfg.SchemasPath = ""

	// 选 Scalar 时关掉 Huma 内置 docs（默认 Stoplight Elements），避免与自托管 Scalar 抢占 /api/docs；
	// 默认（Stoplight）保留 Huma 内置 docs 路由。spec 路由（/api/openapi.json|.yaml）始终由 humagin 注册。
	useScalar := docs == DocsRendererScalar
	if useScalar {
		cfg.DocsPath = ""
	}

	api := humagin.NewWithGroup(engine, group, cfg)
	api.UseMiddleware(injectLocalizer)
	if useScalar {
		registerScalarDocs(group, basePath)
	}
	return api
}

// Secured 声明 operation 需要 bearer 鉴权 + 指定 scope（仅用于 OpenAPI 文档展示；
// 实际拦截由 router 层用 Bridge 包裹的 RequireAuth/RequireScopes 完成）。
func Secured(scopes ...string) []map[string][]string {
	if scopes == nil {
		scopes = []string{}
	}
	return []map[string][]string{{SecuritySchemeBearer: scopes}}
}
