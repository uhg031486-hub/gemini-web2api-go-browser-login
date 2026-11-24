package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/google/uuid"
)

// Gemini 服务端认的模型 id，来自 batchexecute?rpcids=otAQ7b 返回的权威清单。
const (
	hexFlash36   = "fbb127bbb056c959" // 3.6 Flash
	hexFlashLite = "cf41b0e0dd7d53e5" // 3.5 Flash-Lite
	hexPro31     = "9d8ca3786ebdfbea" // 3.1 Pro
	// 3.7 Flash：按账号灰度放出。hex 是 otAQ7b 里 3.7 条目的**主 hex**（第一个元素），
	// 两个独立已灰度账号的清单里都是它、且有 3.7 号的用户实测发它回报 "3.7 Flash"
	// （issue #4 / PR #11）。注意别用 compat 列表里那个 797f3d0293f288ad —— 那是
	// "当前 Flash" 泛指针，老批次号发它拿到的是 3.6，会冒充 3.7。老批次号发这个主 hex
	// 会干净降级成 3.5 Flash-Lite（跟 3.1 Pro 一样），所以 gate 成要 cookie。
	hexFlash38 = "56fdd199312815e2" // 3.8 Flash（原记 3.7；Google 把这个 hex 原地升成 3.8，HAR 响应帧显示名已是 "3.8 Flash"）
	// 4 Argon（#35）：otAQ7b 清单里主 hex 95a4c0abcba24364、短名 Argon、代号 Argon4p0、
	// 显示名 "4 Argon"。付费/Ultra 号专属。实测**真正选中它靠的是 MODE=8（header 下标 14 /
	// inner[79]），不是 hex**：借来的 Ultra 号上，hex=Argon 配 mode=1 回报 "3.8 Flash"（mode 1
	// 那个模型），hex=3.8Flash 配 mode=8 反而回报 "4 Argon"——mode 决定、hex 被忽略。所以 Argon
	// 条目的 Mode 必须填 8。仍按浏览器把 hex 配成 Argon 主 hex（最忠实、将来若改回认 hex 也不塌）。
	hexArgon4 = "95a4c0abcba24364"
	// Current Neon primary id; the old Pro id remains a compatibility route.
	hexNeon4 = "e6fa609c3fa255c0"
)

// innerSlots 是 payload 里 inner 数组的长度。浏览器发 97-98 槽，我们原来只开 80，
// 于是 inner[80]（扩展思考）连位置都没有、想填也填不进去。
//
// 加长本身是安全的：曾经把它从 80 加到 102 来试 inner[79] 会不会复活，结论是不会
// —— 模型选择完全由 x-goog-ext-525001261-jspb header 决定，长度不影响。
const innerSlots = 97

// inner[80] / 模型 header 下标 15 的取值：1=普通，2=扩展思考。
//
// **只在登录态生效**：匿名请求带上它服务端静默忽略，回报的仍是普通模型、思考链 0
// 字符（对齐登录态抓包参数复测 3 组，8 次全灭）。所以带这个的模型跟 3.1 Pro 一样，
// 没 cookie 时不暴露。
const (
	thinkingNormal   = 1
	thinkingExtended = 2
	thinkingMedium   = 4
)

// ModelConfig holds the server-side model id plus the legacy MODE_CATEGORY value.
type ModelConfig struct {
	// HexID 走 x-goog-ext-525001261-jspb header，是服务端唯一认的模型开关。
	// 实测：不发这个 header 时 inner[79] 取 1..6 全部落到 3.5 Flash-Lite；
	// header 写 3.6 而 inner[79] 写 6 时拿到的是 3.6 —— header 压过 inner[79]。
	HexID string
	Mode  int
	Desc  string
	// Thinking 为真时填 inner[80]=2，即网页 UI 上的「扩展思考」。跟 HexID 正交
	// —— 三个模型都能开，不是某个专属模型。
	Thinking bool
	// Explicit web tier, overriding the legacy boolean: low=1, medium=4, high=2.
	ThinkWire int
	// Tool 非 0 时填 inner[49]，让服务端换后端模型生成媒体产物（14=生图 Nano Banana
	// / 21=音乐 Lyria）。产物不在 StreamGenerate 响应里，要再走 hNvQHb 拿下载链、
	// 用下载 host 认的 cookie 子集下回原始字节，见 media.go。跟登录态绑定：匿名请求
	// 这个会被静默降级成一句「Are you signed in?」文本。
	Tool int
}

func (mc ModelConfig) thinkWire() int {
	if mc.ThinkWire > 0 {
		return mc.ThinkWire
	}
	if mc.Thinking {
		return thinkingExtended
	}
	return thinkingNormal
}

// Both fresh requests and conversation continuations must encode the same tier.
func setThinkingFields(inner []interface{}, mc ModelConfig) {
	inner[80] = mc.thinkWire()
	inner[96] = 0
	if mc.thinkWire() != thinkingNormal {
		inner[96] = 1
	}
}

// 只暴露服务端清单（batchexecute?rpcids=otAQ7b）里真实存在的模型。
// 旧的 gemini-3.5-flash / -thinking / -thinking-lite / gemini-auto /
// gemini-flash-lite 别名已移除：它们在服务端没有对应条目，留着只会让人
// 以为有五种不同的模型可选。
var Models = map[string]ModelConfig{
	"gemini-auto":                {HexID: hexFlashLite, Mode: 6, ThinkWire: thinkingNormal, Desc: "Web Auto route; no selectable thinking tier; Google chooses the backend"},
	"gemini-4-hydrogen":          {HexID: hexFlash38, Mode: 1, Desc: "4 Hydrogen; default Low thinking; signed-in account required"},
	"gemini-4-hydrogen-low":      {HexID: hexFlash38, Mode: 1, ThinkWire: thinkingNormal, Desc: "4 Hydrogen — Low thinking"},
	"gemini-4-hydrogen-medium":   {HexID: hexFlash38, Mode: 1, ThinkWire: thinkingMedium, Desc: "4 Hydrogen — Medium thinking"},
	"gemini-4-hydrogen-high":     {HexID: hexFlash38, Mode: 1, ThinkWire: thinkingExtended, Thinking: true, Desc: "4 Hydrogen — High thinking"},
	"gemini-4-hydrogen-thinking": {HexID: hexFlash38, Mode: 1, Thinking: true, Desc: "Alias of gemini-4-hydrogen-high"},
	"gemini-4-neon":              {HexID: hexNeon4, Mode: 3, Desc: "4 Neon; default Low thinking; eligible signed-in account required"},
	"gemini-4-neon-low":          {HexID: hexNeon4, Mode: 3, ThinkWire: thinkingNormal, Desc: "4 Neon — Low thinking"},
	"gemini-4-neon-medium":       {HexID: hexNeon4, Mode: 3, ThinkWire: thinkingMedium, Desc: "4 Neon — Medium thinking"},
	"gemini-4-neon-high":         {HexID: hexNeon4, Mode: 3, ThinkWire: thinkingExtended, Thinking: true, Desc: "4 Neon — High thinking"},
	"gemini-4-neon-thinking":     {HexID: hexNeon4, Mode: 3, Thinking: true, Desc: "Alias of gemini-4-neon-high"},
	"gemini-3.6-flash":           {HexID: hexFlash36, Mode: 1, Desc: "Latest all-around model"},
	"gemini-3.5-flash-lite":      {HexID: hexFlashLite, Mode: 6, Desc: "Fastest, lightweight"},
	"gemini-3.1-pro":             {HexID: hexPro31, Mode: 3, Desc: "Most capable; needs a signed-in cookie (downgraded to Flash-Lite without one)"},
	// 3.8 Flash：Google 把原 3.7 那个 hex 原地升级了（同 hex，服务端显示名从 3.7 变 3.8）。
	// 实测是**付费号专属**（不是灰度）：只有付费 Gemini 账号有，免费号降级成 3.5 Flash-Lite。
	// gemini-3.7-flash 保留为别名（同 hex）。
	"gemini-3.8-flash": {HexID: hexFlash38, Mode: 1, Desc: "3.8 Flash; needs a signed-in PAID Google account (free accounts get downgraded to 3.5 Flash-Lite)"},
	"gemini-3.7-flash": {HexID: hexFlash38, Mode: 1, Desc: "alias of gemini-3.8-flash (same hex; Google renamed 3.7→3.8)"},
	// 4 Argon（#35）：Mode=8 是真正的选择开关（见 hexArgon4 注释）。付费/Ultra 号专属，
	// 免费号发它会被静默降级成 3.5 Flash-Lite，所以 gate 成要 cookie（modelNeedsLogin）。
	"gemini-4-argon": {HexID: hexArgon4, Mode: 8, Desc: "4 Argon (complex tasks); needs a signed-in PAID/Ultra Google account (free accounts get downgraded to 3.5 Flash-Lite)"},

	// 扩展思考版。inner[80]=2 跟模型 hex 正交，都能开；但只在登录态生效，
	// 所以跟 3.1 Pro 一样在没 cookie 时不暴露。
	"gemini-3.6-flash-thinking":      {HexID: hexFlash36, Mode: 1, Thinking: true, Desc: "3.6 Flash with extended thinking; needs a signed-in cookie"},
	"gemini-3.5-flash-lite-thinking": {HexID: hexFlashLite, Mode: 6, Thinking: true, Desc: "3.5 Flash-Lite with extended thinking; needs a signed-in cookie"},
	"gemini-3.1-pro-thinking":        {HexID: hexPro31, Mode: 3, Thinking: true, Desc: "3.1 Pro with extended thinking; needs a signed-in cookie"},
	"gemini-3.8-flash-thinking":      {HexID: hexFlash38, Mode: 1, Thinking: true, Desc: "3.8 Flash with extended thinking; needs a signed-in cookie"},
	"gemini-3.7-flash-thinking":      {HexID: hexFlash38, Mode: 1, Thinking: true, Desc: "alias of gemini-3.8-flash-thinking"},
	// 4 Argon + 扩展思考：Mode=8（选 Argon）+ inner[80]=2（上游现在把这档叫 "High"）。
	// 实测回报 "4 Argon High" 且带思考链（硬题 985 字符，走既有 [4][0][37][0][0] 路径，
	// 无需改抽取代码）→ 映射成 reasoning_content。付费/Ultra 专属。
	"gemini-4-argon-thinking": {HexID: hexArgon4, Mode: 8, Thinking: true, Desc: "4 Argon with extended thinking (High reasoning tier); emits reasoning_content; needs a signed-in PAID/Ultra account"},

	// 媒体生成。inner[49] 一填，服务端换后端模型出图/出乐；产物走 hNvQHb + 下载 host
	// 取回，以 base64 data URL 塞进 content 返回。都要登录态，没 cookie 时不暴露。
	"gemini-image": {HexID: hexFlash36, Mode: 1, Tool: toolImage, Desc: "Image generation (Nano Banana); returns a base64 data URL; needs a signed-in cookie"},
	"gemini-music": {HexID: hexFlash36, Mode: 1, Tool: toolMusic, Desc: "Music generation (Lyria, ~30s); returns a base64 data URL; needs a signed-in cookie"},
	"gemini-video": {HexID: hexFlash36, Mode: 1, Tool: toolVideo, Desc: "Video generation (Veo, async); returns a base64 data URL; needs a signed-in cookie (usually a paid account)"},
	// 画布：生成 immersive 交互 HTML 文档，内联返回（不是二进制、不用下载）。要登录态。
	"gemini-canvas": {HexID: hexFlash36, Mode: 1, Tool: toolCanvas, Desc: "Canvas: generates an interactive HTML document (returned inline as a ```html block); needs a signed-in cookie"},
}

// hasCookie 表示 cookie 池里有没有可用账号。决定 3.1 Pro 是否出现在模型列表里。
func hasCookie() bool {
	_, enabled := accountCount()
	return enabled > 0
}

// modelNeedsLogin 判断一个模型是否必须登录态才真正生效。匿名请求这批会被服务端
// 静默降级：3.1 Pro / 3.8 Flash → 3.5 Flash-Lite；思考链消失；媒体工具变成一句
// "Are you signed in?" 文本。是「没 cookie 时排除哪些模型」和「#20 匿名优先要不要
// 占一个 cookie 账号」共用的单一判据。
//
// 按 HexID + Thinking + Tool 判，跟旧的按模型名（3.1-pro/3.8-flash/3.7-flash）判
// 逐模型核对等价：那三个正名的 HexID 就是 hexPro31 / hexFlash38，其余登录模型都被
// Thinking 或 Tool 覆盖。
func modelNeedsLogin(mc ModelConfig) bool {
	return mc.HexID == hexPro31 || mc.HexID == hexFlash38 || mc.HexID == hexArgon4 || mc.HexID == hexNeon4 || mc.Thinking || mc.ThinkWire > 0 || mc.Tool > 0
}

// temporaryChatEligible 判断这次请求该不该走网页端原生「临时对话」（inner[45]=1，#34）。
//
// 与 multi_turn 冲突：临时对话不进会话列表，而 multi_turn 要按历史认出续接、靠服务端
// 留着这串对话——两个一起开，续接那半就没了。所以 multi_turn 开着时一律不发 inner[45]，
// 由 multi_turn 决定会话怎么留。调用点还会再要求有登录 cookie：匿名本来就不落账号，
// 发 inner[45] 是没验过的无谓偏离。
func temporaryChatEligible() bool {
	return rtCfg().TemporaryChat && !rtCfg().MultiTurn
}

// anonFirstEligible 判断「匿名优先」开关下这次请求能不能走匿名（不占 cookie 账号）：
// 开关开着 + 没带附件 + 模型不需要登录态。附件（图/视频）在对话里引用必须登录
// （匿名会被上游回 1100），所以带附件时一律挑号。开关关着永远返回 false（保持旧行为：
// 池里有号就用号）。
func anonFirstEligible(mc ModelConfig, hasAttachment bool) bool {
	return rtCfg().AnonFirst && !hasAttachment && !modelNeedsLogin(mc)
}

// availableModels 返回当前配置下值得暴露的模型。
//
// 没配 cookie 时排除 3.1 Pro：实测匿名请求它会被静默降级成 3.5 Flash-Lite，
// 客户端还以为自己用上了 Pro。与其让它"成功"，不如直接不提供、让选型时就报错。
//
// 配了有效 cookie 时它是真能用的：连打 6 次服务端回报的都是 "3.1 Pro" 本身，
// 且每次都带思考链（118-152 字符，普通 3.6 Flash 为 0）。早前记录的「免费号
// 登录也只能拿到 3.6 Flash 扩展」是在缺 XSRF token 的条件下测的，那时候带
// cookie 的请求根本发不出去（见 xsrf.go）。
func availableModels() map[string]ModelConfig {
	if hasCookie() {
		return Models
	}
	out := make(map[string]ModelConfig, len(Models))
	for k, v := range Models {
		if modelNeedsLogin(v) {
			continue
		}
		out[k] = v
	}
	return out
}

// resolveModel maps a model name to its config.
//
// "name@think=N" 后缀会被剥掉并忽略。旧版本把它写进 inner[17] 当思考深度，
// 那是误读：抓包显示 inner[17] 是会话内的轮次索引（首轮 [[0]]，带会话 id 的
// 第二轮 [[1]]，逐轮递增），跟思考深度无关。我们每次都开新会话，该值恒为 0。
// 后缀不报错只忽略，避免打断已经配了这个写法的客户端。
func resolveModel(modelName string) (string, ModelConfig, error) {
	if idx := strings.Index(modelName, "@think="); idx >= 0 {
		modelName = modelName[:idx]
	}
	mc, ok := availableModels()[modelName]
	if !ok {
		if full, exists := Models[modelName]; exists && !hasCookie() {
			downgrade := "are silently downgraded to 3.5 Flash-Lite"
			if full.Tool > 0 {
				downgrade = "are silently downgraded to a plain \"Are you signed in?\" text reply"
			}
			return "", ModelConfig{}, fmt.Errorf(
				"%s is unavailable without a Google account cookie: anonymous requests for it "+
					"%s. Add a cookie in the admin panel (Cookie pool) or via --cookie-file to enable it",
				modelName, downgrade)
		}
		return "", ModelConfig{}, fmt.Errorf("unknown model: %s", modelName)
	}
	return modelName, mc, nil
}

// StreamResult holds raw body + per-request proxy + timing info.
type StreamResult struct {
	// Emitted 是流式模式下已经通过 onDelta 发出去的文本；非流式为空。
	Emitted string
	Raw     string
	// Reasoning 是模型的思考链（只有 3.1 Pro 会产出）。
	// 上游每次都发，我们以前只取正文、把它扔了。
	Reasoning string
	// EmittedReasoning 是流式下已经通过 onReasoning 发出去的思考链；非流式为空。
	EmittedReasoning string
	// UpstreamModel 是服务端在响应帧 [42] 里自报的模型显示名。
	// 跟请求的模型未必一致：gemini-3.1-pro 匿名时被静默降级成 3.5 Flash-Lite，
	// 只看请求名根本发现不了，所以这个字段要一直记着。
	UpstreamModel string
	ProxyID       int64
	ProxyName     string
	// 用了 cookie 池里的哪个账号，0 = 匿名。失败的请求也要带上——
	// 排查"加了 cookie 就大面积失败"时，最需要知道的正是失败那条用的哪个号。
	AccountID    int64
	AccountLabel string
	TTFBMs       int64
	TotalMs      int64
	// Artifacts 是媒体模型（生图/音乐）取回的产物原始字节，非媒体模型为空。
	Artifacts []MediaArtifact
	// MediaErr 记媒体产物取回失败的原因：生成本身 200 了、但走 hNvQHb / 下载那步挂了。
	// 调用方据此报错，而不是返回一个只有文字没有图的「半成功」。
	MediaErr string
}

// RateLimitError 表示所有 IP slot 都达到了限流上限。
// HTTP handler 看到这个错时返回 429 给客户端。
type RateLimitError struct {
	Reason  string // "concurrent" / "rpm" / "rph"
	ProxyID int64  // 0 = 直连 slot 满
}

func (e *RateLimitError) Error() string {
	if e.ProxyID == 0 {
		return "direct IP slot full: " + e.Reason + " limit reached (configure proxies to scale)"
	}
	return "all proxy slots full: " + e.Reason + " limit reached"
}

// acquireSlot 选一个有容量的 slot 给本次请求用。
// 优先级：代理池里有容量的代理 → 直连。
// 全满返回 *RateLimitError。
//
// 调用方拿到 (proxy, ok=true) 必须配 deferred releaseSlot()。
func acquireSlot(preferProxyID int64) (Proxy, bool, error) {
	// 1. 先试代理池（如果配了）
	proxyMu.RLock()
	hasProxies := len(proxyCache) > 0
	proxyMu.RUnlock()

	if hasProxies {
		if p, ok := pickProxyPreferring(preferProxyID); ok {
			return p, true, nil
		}
		// 代理池里有代理但一个都用不上（限流满 / 全禁用 / 全熔断且没过冷却）。
		// 默认**不退回直连** —— 配了代理池就意味着不想让上游看到本机 IP，
		// 悄悄直连等于把这个前提废掉，而且日志上只是几条普通请求，很难发现。
		// 要可用性优先于隐藏 IP 的部署可以打开 fallback_direct。
		if !rtCfg().FallbackDirect {
			return Proxy{}, false, &RateLimitError{Reason: "rph", ProxyID: -1}
		}
		if ok, reason := trySlotAcquire(0); ok {
			logf("[proxy] 代理池无可用出口，本次退回直连（fallback_direct 已开）")
			return Proxy{}, true, nil
		} else {
			return Proxy{}, false, &RateLimitError{Reason: reason, ProxyID: 0}
		}
	}

	// 2. 没配代理池 → 用直连 slot（id=0）
	if ok, reason := trySlotAcquire(0); ok {
		return Proxy{}, true, nil // ProxyID=0 表示直连
	} else {
		return Proxy{}, false, &RateLimitError{Reason: reason, ProxyID: 0}
	}
}

// 一个请求最多试几个 cookie。池子大时挨个试到底会让失败请求拖很久，
// 而连试 3 个都不行基本说明是池子整体的问题，不是撞上个别坏号。
const maxCookieTries = 3

// releaseSlot 释放占用。proxyID=0 表示直连。
func releaseSlot(proxyID int64) {
	slotRelease(proxyID)
}

// deltaTracker 把上游的累积帧转成增量。
//
// 上游每帧带的是**到目前为止的全文**，不是新增部分，所以要跟已发出的做前缀
// 比对。帧之间偶尔不满足前缀关系（模型改写、或 clean 掉的 artifact 落在边界
// 上），这时宁可跳过也不能发——发了就等于把重复内容推给客户端，而已发出的
// 内容收不回来。漏掉的部分由调用方在结束时用 remainingText 补齐。
type deltaTracker struct{ emitted string }

// Push 吃进一帧的累积全文，返回相对上一次的增量；没有新增或无法安全 diff 时返回 ""。
func (d *deltaTracker) Push(fullText string) string {
	cleaned := cleanGeminiText(fullText)
	if len(cleaned) <= len(d.emitted) || !strings.HasPrefix(cleaned, d.emitted) {
		return ""
	}
	delta := cleaned[len(d.emitted):]
	d.emitted = cleaned
	return delta
}

// streamGenerate POSTs to Gemini's StreamGenerate endpoint and returns raw body
// plus proxy/timing telemetry for the metrics layer.
// The 80-slot inner array is verbatim from the Python reference.
// onDelta 非 nil 时开启真流式：上游每写一帧就解析一次，跟已发出的内容做前缀
// diff，把新增部分立刻回调出去。上游每帧带的是累积全文而不是增量，diff 必须
// 自己做。一旦已经吐过内容就不再重试——重试会让客户端收到重复文本。
func streamGenerate(prompt, latest string, mc ModelConfig,
	onDelta, onReasoning func(string)) (*StreamResult, error) {
	return streamGenerateWithFiles(prompt, latest, mc, nil, onDelta, onReasoning)
}

// fileRef 是一个已上传附件的引用。
type fileRef struct {
	Ref  string // 上传返回的路径，形如 /contrib_service/ttl_1d/…
	Name string // 展示给模型看的文件名
	Kind int    // 附件类型：1=图片，3=文本/普通文件
	Mime string // 内容类型，服务端按它决定怎么解析附件
}

var (
	getXSRFFn     = getXSRF
	doGeminiReqFn = doGeminiRequest
	retrySleepFn  = time.Sleep
)

var upstreamSoftErrors = []string{
	"i seem to be encountering an error. can i try something else for you?",
	"i encountered an error doing what you asked. could you try again?",
	"something went wrong and i wasn't able to generate a response.",
	"something went wrong. please try again.",
	"i'm having a hard time fulfilling your request. can i help you with something else instead?",
	"sorry, something went wrong. please try your request again.",
}

func isUpstreamSoftError(text string) bool {
	cleaned := strings.ToLower(strings.TrimSpace(text))
	if cleaned == "" {
		return false
	}
	for _, msg := range upstreamSoftErrors {
		if cleaned == msg {
			return true
		}
	}
	return false
}

func isUpstreamSoftErrorPrefix(text string) bool {
	cleaned := strings.ToLower(strings.TrimSpace(text))
	if cleaned == "" {
		return false
	}
	for _, msg := range upstreamSoftErrors {
		if strings.HasPrefix(msg, cleaned) {
			return true
		}
	}
	return false
}

type resolvedCookieAuth struct {
	acct      *CookieAccount
	id        int64
	label     string
	cookie    string
	sapisid   string
	xsrf      string
	authUser  int
}

func resolveWorkingCookie(acct *CookieAccount, proxyURL string, pickedID int64,
	tried, rotatedOnce map[int64]bool, activeAcctID *int64) (resolvedCookieAuth, error) {
	var res resolvedCookieAuth
	var lastErr error
	for tries := 0; acct != nil && tries < maxCookieTries; tries++ {
		tried[acct.ID] = true
		res.acct = acct
		res.id, res.label = acct.ID, accountDisplayName(acct)
		tok, err := getXSRFFn(acct.Cookie, proxyURL, acct.AuthUser)
		if err == nil {
			res.cookie = acct.Cookie
			res.sapisid = extractSAPISID(acct.Cookie)
			res.xsrf = tok
			res.authUser = acct.AuthUser
			if acct.ProxyID == 0 || !proxyUsableByID(acct.ProxyID) {
				bindAccountProxy(acct.ID, pickedID)
			}
			return res, nil
		}
		if !rotatedOnce[acct.ID] {
			rotatedOnce[acct.ID] = true
			if !browserManagedCookie(*acct) {
				if _, _, rerr := rotateAccount(*acct); rerr == nil {
					if fresh := accountByID(acct.ID); fresh != nil {
						if tok2, err2 := getXSRFFn(fresh.Cookie, proxyURL, fresh.AuthUser); err2 == nil {
							logf("[cookie] 账号 #%d 轮转后恢复可用", acct.ID)
							res.acct = fresh
							res.id, res.label = fresh.ID, accountDisplayName(fresh)
							res.cookie = fresh.Cookie
							res.sapisid = extractSAPISID(fresh.Cookie)
							res.xsrf = tok2
							res.authUser = fresh.AuthUser
							if fresh.ProxyID == 0 || !proxyUsableByID(fresh.ProxyID) {
								bindAccountProxy(fresh.ID, pickedID)
							}
							return res, nil
						}
					}
				}
			}
		}
		markCookieByStatus(acct.ID, 401, err.Error())
		lastErr = err
		logf("[cookie] 账号 #%d 不可用，换下一个：%v", acct.ID, err)
		releaseAccountInflight(*activeAcctID)
		*activeAcctID = 0
		acct, _ = pickCookieAccountExcept(tried)
		if acct != nil {
			*activeAcctID = acct.ID
		}
	}
	return res, lastErr
}

type streamAttemptPayload struct {
	endpoint      string
	headers       map[string]string
	modelHeader   string
	sessionHeader string
	body          string
	buildBody     func(string) string
}

func buildStreamAttemptPayload(origPrompt, latest string, mc ModelConfig, pending []pendingUpload,
	cookieStr, sapisid, xsrfToken, proxyURL string, authUser int, bl string, reqid int64) (*streamAttemptPayload, error) {
	prompt := origPrompt
	authSlot := authUser
	if cookieStr == "" {
		authSlot = 0
	}
	endpoint := fmt.Sprintf(
		"https://gemini.google.com%s/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?bl=%s&hl=en&_reqid=%d&rt=c",
		geminiURLPrefix(authSlot), bl, reqid,
	)

	var files []fileRef
	if len(pending) > 0 {
		if cookieStr == "" {
			return nil, fmt.Errorf("image input needs a Google account cookie: " +
				"anonymous uploads succeed but referencing them in a conversation is " +
				"rejected upstream. Add a cookie in the admin panel (Cookie pool)")
		}
		nImg, nVid := 0, 0
		for _, u := range pending {
			ref, uerr := uploadBytes(cookieStr, proxyURL, authSlot, u.Data, u.Name)
			if uerr != nil {
				return nil, fmt.Errorf("上传附件 %s 失败: %w", u.Name, uerr)
			}
			files = append(files, fileRef{Ref: ref, Name: u.Name, Kind: u.Kind, Mime: u.Mime})
			if u.Kind == 2 {
				nVid++
			} else {
				nImg++
			}
		}
		logf("[vision] 上传了 %d 张图 / %d 个视频", nImg, nVid)
	}

	budget := rtCfg().MaxPromptBytes
	if p, f, used, ferr := prepareContextFile(prompt, latest, budget, cookieStr, proxyURL, authSlot); ferr != nil {
		return nil, ferr
	} else if used {
		prompt = p
		files = append(files, f...)
	}
	if budget > 0 && len(prompt) > budget {
		return nil, &PromptTooLongError{
			Bytes: len(prompt), Budget: budget, HasCookie: cookieStr != "",
		}
	}

	inner := make([]interface{}, innerSlots)
	if len(files) > 0 {
		refs := make([]interface{}, 0, len(files))
		for _, f := range files {
			kind := f.Kind
			if kind == 0 {
				kind = 3
			}
			mime := f.Mime
			if mime == "" {
				mime = "text/plain"
			}
			refs = append(refs, []interface{}{
				[]interface{}{f.Ref, kind, nil, mime}, f.Name,
				nil, nil, nil, nil, nil, nil,
				[]interface{}{0},
			})
		}
		inner[0] = []interface{}{prompt, 0, nil, refs, nil, nil, 0}
	} else {
		inner[0] = []interface{}{prompt, 0, nil, nil, nil, nil, 0}
	}
	inner[1] = []interface{}{"en"}
	inner[2] = []interface{}{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	inner[6] = []interface{}{0}
	inner[7] = 1
	inner[10] = 1
	inner[11] = 0
	inner[17] = []interface{}{[]interface{}{0}}
	inner[18] = 0
	inner[27] = 1
	inner[30] = []interface{}{4}
	inner[41] = []interface{}{1}
	if temporaryChatEligible() && cookieStr != "" {
		inner[45] = 1
	}
	inner[53] = 0
	reqUUID := uuid.NewString()
	inner[59] = reqUUID
	inner[61] = []interface{}{}
	inner[68] = 1
	inner[79] = mc.Mode
	inner[91] = 0
	setThinkingFields(inner, mc)
	if mc.Tool > 0 {
		inner[49] = mc.Tool
	}
	if mc.Tool == toolVideo {
		inner[55] = []interface{}{[]interface{}{16}}
	}

	innerJSON, err := json.Marshal(inner)
	if err != nil {
		return nil, err
	}
	outer := []interface{}{nil, string(innerJSON)}
	outerJSON, err := json.Marshal(outer)
	if err != nil {
		return nil, err
	}

	buildBody := func(at string) string {
		form := url.Values{}
		form.Set("f.req", string(outerJSON))
		if at != "" {
			form.Set("at", at)
		}
		return form.Encode()
	}

	modelHeader := buildModelHeader(mc.HexID, mc.Mode, mc.thinkWire(), uuid.NewString())
	sessionHeader := fmt.Sprintf(`["%s",1]`, reqUUID)
	headers := buildGeminiHeaders(cookieStr, sapisid, mc.HexID, authSlot)
	headers["x-goog-ext-525001261-jspb"] = modelHeader
	headers["x-goog-ext-525005358-jspb"] = sessionHeader

	return &streamAttemptPayload{
		endpoint:      endpoint,
		headers:       headers,
		modelHeader:   modelHeader,
		sessionHeader: sessionHeader,
		body:          buildBody(xsrfToken),
		buildBody:     buildBody,
	}, nil
}

// streamGenerateWithFiles 同上，但可以带附件。
//
// 附件填 inner[0][3]，形状 [[[ref, 1], "文件名"], …]。附件只在登录态可用：
// 匿名能把文件传上去，但对话里一引用就被服务端回 1100。
func streamGenerateWithFiles(prompt, latest string, mc ModelConfig, pending []pendingUpload,
	onDelta, onReasoning func(string)) (*StreamResult, error) {
	// 先挑号，再按它上次绑的出口挑代理 —— 同一个账号要尽量固定从同一个 IP 出去，
	// 否则一个号在几十个出口之间跳，在 Google 眼里就是账号共享的特征。
	// #20 匿名优先：不需要登录态能力时不占用 cookie 账号，走匿名省额度。
	var acct *CookieAccount
	var activeAcctID int64
	if !anonFirstEligible(mc, len(pending) > 0) {
		if a, ok := pickCookieAccount(); ok {
			acct = a
			activeAcctID = a.ID
		}
	}
	defer func() {
		releaseAccountInflight(activeAcctID)
	}()
	preferProxy := int64(0)
	if acct != nil {
		preferProxy = acct.ProxyID
	}

	// 出错时也要把「用了哪个号 / 哪个出口」带回去，否则失败记录里全是空白。
	var picked Proxy
	var cookieID int64
	var cookieLabel string
	attrib := func(err error) (*StreamResult, error) {
		return &StreamResult{
			AccountID: cookieID, AccountLabel: cookieLabel,
			ProxyID: picked.ID, ProxyName: picked.Name,
		}, err
	}

	p, slotOK, slotErr := acquireSlot(preferProxy)
	if !slotOK {
		return attrib(slotErr)
	}
	picked = p
	defer releaseSlot(picked.ID)

	proxyURL := picked.URL
	pickedOK := picked.ID > 0

	reqid := time.Now().Unix() % 1000000
	bl := currentBL(proxyURL)
	if mc.Tool > 0 && !rtCfg().MediaUseAutoBL {
		bl = currentBLPinned()
	}

	tried := map[int64]bool{}
	rotatedOnce := map[int64]bool{}
	auth, lastCookieErr := resolveWorkingCookie(acct, proxyURL, picked.ID, tried, rotatedOnce, &activeAcctID)
	acct = auth.acct
	cookieID, cookieLabel = auth.id, auth.label
	cookieStr, sapisid, xsrfToken, authUser := auth.cookie, auth.sapisid, auth.xsrf, auth.authUser
	if lastCookieErr != nil && cookieStr == "" {
		releaseAccountInflight(activeAcctID)
		activeAcctID = 0
		if !rtCfg().FallbackAnon {
			return attrib(fmt.Errorf("cookie 池里 %d 个账号都不可用（最后一个：%w）；"+
				"到面板「Cookie 池」用「检测」按钮逐个排查，或打开 fallback_anon 降级匿名",
				len(tried), lastCookieErr))
		}
		logf("[cookie] 试过的 %d 个账号都不可用，本次降级匿名（能力会退化到匿名档）", len(tried))
		cookieID, cookieLabel = 0, ""
	}

	origPrompt := prompt
	payload, err := buildStreamAttemptPayload(origPrompt, latest, mc, pending,
		cookieStr, sapisid, xsrfToken, proxyURL, authUser, bl, reqid)
	if err != nil {
		return attrib(err)
	}

	var lastErr error
	lastStatus := 0
	xsrfRetried := false
	t0 := time.Now()

	tracker := &deltaTracker{}
	rtracker := &deltaTracker{}
	var lineCB func(string)
	if onDelta != nil || onReasoning != nil {
		lineCB = func(line string) {
			if onReasoning != nil {
				if r := reasoningInLine(line); r != "" {
					if d := rtracker.Push(r); d != "" {
						onReasoning(d)
					}
				}
			}
			if onDelta == nil {
				return
			}
			for _, t := range textsInLine(line) {
				// Giữ lại prefix của câu báo lỗi mềm từ Google khi chưa phát gì ra stream,
				// tránh làm bẩn SSE client trước khi proxy tự đổi tài khoản retry.
				if tracker.emitted == "" && isUpstreamSoftErrorPrefix(cleanGeminiText(t)) {
					continue
				}
				if d := tracker.Push(t); d != "" {
					onDelta(d)
				}
			}
		}
	}

	retryBudget := rtCfg().RetryAttempts
	if retryBudget < 1 {
		retryBudget = 1
	}
	if mc.Tool > 0 && retryBudget > 2 {
		retryBudget = 2
	}

	rotateAccountOrSleep := func(attempt int) {
		if attempt >= retryBudget-1 {
			return
		}
		if cookieStr != "" {
			if nextAcct, ok := pickCookieAccountExcept(tried); ok && nextAcct != nil {
				releaseAccountInflight(activeAcctID)
				activeAcctID = nextAcct.ID
				nextAuth, rerr := resolveWorkingCookie(nextAcct, proxyURL, picked.ID, tried, rotatedOnce, &activeAcctID)
				if rerr == nil && nextAuth.cookie != "" {
					nextPayload, perr := buildStreamAttemptPayload(origPrompt, latest, mc, pending,
						nextAuth.cookie, nextAuth.sapisid, nextAuth.xsrf, proxyURL, nextAuth.authUser, bl, reqid+int64(attempt+1))
					if perr == nil {
						acct = nextAuth.acct
						cookieID, cookieLabel = nextAuth.id, nextAuth.label
						cookieStr, sapisid, xsrfToken, authUser = nextAuth.cookie, nextAuth.sapisid, nextAuth.xsrf, nextAuth.authUser
						payload = nextPayload
						xsrfRetried = false
						logf("[retry] %d/%d: chuyển sang tài khoản #%d (%s)", attempt+1, retryBudget, cookieID, cookieLabel)
						return
					}
				}
			}
		}
		retrySleepFn(time.Duration(rtCfg().RetryDelaySec) * time.Second)
	}

	for attempt := 0; attempt < retryBudget; attempt++ {
		reqTimeout := 0
		if mc.Tool > 0 {
			reqTimeout = mediaTimeoutSec
		}
		statusCode, raw, ttfb, setCookie, err := doGeminiReqFn(payload.endpoint, payload.body, payload.headers, proxyURL, lineCB, reqTimeout)
		if len(setCookie) > 0 && cookieID > 0 {
			if merged := mergeSetCookie(cookieStr, setCookie); merged != cookieStr {
				cookieStr = merged
				updateAccountCookie(cookieID, merged)
			}
		}
		if err != nil {
			lastErr = err
			if pickedOK {
				if !(mc.Tool > 0 && isTimeoutError(err)) {
					recordProxyResult(picked.ID, false, err.Error())
				}
			}
			if tracker.emitted != "" || rtracker.emitted != "" {
				break
			}
			if mc.Tool > 0 && isTimeoutError(err) {
				logf("媒体请求超时（%v），不重试、不计代理失败（已等满 %d 秒预算）", err, mediaTimeoutSec)
				break
			}
			logf("retry %d/%d: %v", attempt+1, retryBudget, err)
			rotateAccountOrSleep(attempt)
			continue
		}
		if statusCode != 200 {
			if statusCode == 400 && isXSRFError(string(raw)) && cookieStr != "" && !xsrfRetried {
				xsrfRetried = true
				invalidateXSRF(cookieStr, authUser)
				if tok, e := getXSRFFn(cookieStr, proxyURL, authUser); e == nil {
					xsrfToken = tok
					payload.body = payload.buildBody(tok)
					payload.headers = buildGeminiHeaders(cookieStr, sapisid, mc.HexID, authUser)
					payload.headers["x-goog-ext-525001261-jspb"] = payload.modelHeader
					payload.headers["x-goog-ext-525005358-jspb"] = payload.sessionHeader
					attempt--
					continue
				}
			}
			lastErr = fmt.Errorf("upstream HTTP %d: %s", statusCode, truncate(string(raw), 200))
			lastStatus = statusCode
			if pickedOK {
				recordProxyResult(picked.ID, false, lastErr.Error())
			}
			if tracker.emitted != "" || rtracker.emitted != "" {
				break
			}
			rotateAccountOrSleep(attempt)
			continue
		}
		videoAccepted := mc.Tool == toolVideo && extractConversationID(string(raw)) != ""
		if videoAccepted && !hasContentFrame(string(raw)) {
			logf("[media] 视频请求已受理（无正文帧，异步生成），转入产物轮询 cid=%s", extractConversationID(string(raw)))
		}
		if !hasContentFrame(string(raw)) && !videoAccepted {
			lastErr = fmt.Errorf("upstream returned no content frame (raw %d bytes)", len(raw))
			lastStatus = 0
			if pickedOK {
				recordProxyResult(picked.ID, false, lastErr.Error())
			}
			if tracker.emitted != "" || rtracker.emitted != "" {
				break
			}
			logf("空响应原文（%d 字节）: %s", len(raw), truncate(strings.ReplaceAll(string(raw), "\n", " "), 300))
			logf("retry %d/%d: 空响应（无内容帧，%d 字节）", attempt+1, retryBudget, len(raw))
			rotateAccountOrSleep(attempt)
			continue
		}
		if mc.Tool == 0 {
			if softText := extractResponseText(string(raw)); isUpstreamSoftError(softText) {
				lastErr = fmt.Errorf("upstream soft error (acct=#%d): %s", cookieID, truncate(softText, 120))
				lastStatus = 0
				markCookieByStatus(cookieID, 0, lastErr.Error())
				if tracker.emitted != "" || rtracker.emitted != "" {
					break
				}
				logf("retry %d/%d: 上游软错误 (acct=#%d): %s", attempt+1, retryBudget, cookieID, truncate(softText, 120))
				rotateAccountOrSleep(attempt)
				continue
			}
		}
		if pickedOK {
			recordProxyResult(picked.ID, true, "")
		}
		markCookieByStatus(cookieID, 200, "")
		result := &StreamResult{
			Emitted:          tracker.emitted,
			EmittedReasoning: rtracker.emitted,
			Raw:              string(raw),
			Reasoning:        extractReasoning(string(raw)),
			UpstreamModel:    extractUpstreamModel(string(raw)),
			ProxyID:          picked.ID,
			ProxyName:        picked.Name,
			AccountID:        cookieID,
			AccountLabel:     cookieLabel,
			TTFBMs:           ttfb,
			TotalMs:          time.Since(t0).Milliseconds(),
		}
		if mc.Tool == toolImage || mc.Tool == toolMusic || mc.Tool == toolVideo {
			mime := "image/png"
			switch mc.Tool {
			case toolMusic:
				mime = "audio/mpeg"
			case toolVideo:
				mime = "video/mp4"
			}
			remaining := mediaTimeoutSec - int(time.Since(t0).Seconds())
			if remaining < 30 {
				remaining = 30
			}
			arts, aerr := fetchMediaArtifactsBudget(
				mc.Tool, string(raw), extractConversationID(string(raw)),
				cookieStr, sapisid, xsrfToken, proxyURL, mime, authUser, remaining)
			if aerr != nil {
				_ = os.WriteFile("/tmp/media_raw_last_fail.txt", raw, 0644)
				logf("[media] 取回产物失败: %v（原始响应已存 /tmp/media_raw_last_fail.txt）", aerr)
				result.MediaErr = aerr.Error()
			} else {
				_ = os.WriteFile("/tmp/media_raw_last_ok.txt", raw, 0644)
				result.Artifacts = arts
				logf("[media] 取回 %d 份产物", len(arts))
			}
			result.TotalMs = time.Since(t0).Milliseconds()
		}
		if rtCfg().AutoDeleteConversation && !temporaryChatEligible() && cookieStr != "" && xsrfToken != "" {
			if cid := extractConversationID(string(raw)); cid != "" {
				go deleteConversation(cid, cookieStr, sapisid, xsrfToken, proxyURL, authUser)
			}
		}
		return result, nil
	}
	if lastErr != nil {
		markCookieByStatus(cookieID, lastStatus, lastErr.Error())
	}
	return attrib(lastErr)
}

// upstreamModelRe 匹配响应帧里服务端自报的模型显示名（帧的 [42] 位）。
// 形如 ...,"fbb127bbb056c959",null,null,"3.6 Flash",true,...
var upstreamModelRe = regexp.MustCompile(`\\"[0-9a-f]{16}\\",null,null,\\"([^"\\]{1,40})\\"`)

// extractUpstreamModel 取服务端实际使用的模型名，取不到返回空串。
func extractUpstreamModel(raw string) string {
	m := upstreamModelRe.FindAllStringSubmatch(raw, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// buildModelHeader 拼 x-goog-ext-525001261-jspb，形状逐槽取自抓包：
//
//	[1,null,null,null,"<hex>",null,null,0,[4,5,6,8],null,null,1,null,null,<mode>,<think>,"<uuid>"]
//	下标                4                8                          14      15       16
//
// 下标 14 跟 inner[79] 同值、下标 15 跟 inner[80] 同值 —— 模型和思考模式在 header 和
// payload 里各存一份。**服务端认的是 header**：只填 inner[80]=2 而 header 留最小形式，
// 三个模型实测思考链全是 0 字符；这跟模型选择本身"header 压过 inner[79]"是同一个规律。
//
// 下标 16 是**另一个** uuid，跟 inner[59] 不是一个值 —— 跟 inner[59] 同值的是
// x-goog-ext-525005358-jspb。两份抓包都是这个规律，别图省事复用同一个。
//
// uuid 留空时退回最小形式（匿名路径不需要这些槽位，少发一截更省事）。
func buildModelHeader(hexID string, mode, think int, uuid string) string {
	if uuid == "" {
		return fmt.Sprintf(`[1,null,null,null,"%s"]`, hexID)
	}
	return fmt.Sprintf(
		`[1,null,null,null,"%s",null,null,0,[4,5,6,8],null,null,1,null,null,%d,%d,"%s"]`,
		hexID, mode, think, uuid)
}

// buildGeminiHeaders 准备 StreamGenerate 必需的应用层 header。
// hexID 决定服务端用哪个模型；留空则服务端一律回落到 3.5 Flash-Lite。
//
// authuser 是账号槽位（0=默认账号）。实测切号靠 URL 里的 /u/N/ 路径，这个头
// 不影响结果，但浏览器两个都发，保持一致（见 authuser.go）。
func buildGeminiHeaders(cookieStr, sapisid, hexID string, authuser int) map[string]string {
	h := map[string]string{
		"Accept":          "*/*",
		"Accept-Language": "en-US,en;q=0.9",
		"Content-Type":    "application/x-www-form-urlencoded;charset=UTF-8",
		"Origin":          "https://gemini.google.com",
		"Referer":         geminiPageURL(authuser),
		"X-Same-Domain":   "1",
		"X-Goog-AuthUser": authUserHeaderValue(authuser),
		// 这两个浏览器每次都发，值是固定的。
		"x-goog-ext-73010989-jspb": "[0]",
		"x-goog-ext-73010990-jspb": "[0,0,0]",
	}
	if hexID != "" {
		h["x-goog-ext-525001261-jspb"] = buildModelHeader(hexID, 0, 0, "")
	}
	if cookieStr != "" {
		h["Cookie"] = cookieStr
	}
	if sapisid != "" {
		h["Authorization"] = makeSAPISIDHash(sapisid)
	}
	return h
}

// doGeminiRequest 发一次请求到 endpoint。proxyURL 非空走 stdlib（支持 socks5/http），
// 空走 tls-client（chrome146 真指纹）。返回 (HTTP status, body bytes, err)。
// 返回值多了 setCookie：服务端几乎每个响应都在刷新 SIDCC / __Secure-1PSIDCC /
// __Secure-3PSIDCC，浏览器收下再带回去。一直发旧值的客户端会被判定为过期会话，
// 实测号活一两小时就失效 —— 所以这些必须收下来并写回账号。
func doGeminiRequest(endpoint, body string, headers map[string]string, proxyURL string,
	onLine func(string), timeoutSec int) (int, []byte, int64, []string, error) {
	sendAt := time.Now()
	if proxyURL != "" {
		// 走 stdlib 的 http.ProxyURL，已知能过 socks5/socks5h。
		req, err := http.NewRequest("POST", endpoint, strings.NewReader(body))
		if err != nil {
			return 0, nil, 0, nil, err
		}
		applyChromeHeaders(req)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		client := getStdlibClientTimeout(proxyURL, timeoutSec)
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, 0, nil, err
		}
		defer resp.Body.Close()
		raw, ttfb, err := readBody(resp.Body, onLine, sendAt)
		if err != nil {
			return resp.StatusCode, nil, ttfb, resp.Header.Values("Set-Cookie"), err
		}
		return resp.StatusCode, raw, ttfb, resp.Header.Values("Set-Cookie"), nil
	}

	// 直连 → tls-client，保留 chrome146 TLS/HTTP2 真指纹
	req, err := fhttp.NewRequest("POST", endpoint, strings.NewReader(body))
	if err != nil {
		return 0, nil, 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := getTLSClientTimeout(timeoutSec)
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, 0, nil, err
	}
	defer resp.Body.Close()
	raw, ttfb, err := readBody(resp.Body, onLine, sendAt)
	if err != nil {
		return resp.StatusCode, nil, ttfb, resp.Header.Values("Set-Cookie"), err
	}
	return resp.StatusCode, raw, ttfb, resp.Header.Values("Set-Cookie"), nil
}

// readBody 读完整个响应体并原样返回；onLine 非 nil 时每读到一行就回调一次，
// 让上层能在上游还没写完时就往客户端转发。
//
// 始终走逐行扫描（而不是 onLine==nil 时图省事用 io.ReadAll），因为要拿到
// **第一行到达的时刻**当 TTFB。用 ReadAll 的话读完才返回，测出来的"首字节
// 耗时"实际是完整耗时，跟总耗时永远一样。
//
// start 必须是**请求发出前**的时刻，由调用方传入。放在本函数里取 time.Now()
// 是不对的：那时 client.Do 已经返回、响应头甚至部分 body 都到了，测出来恒为 0。
func readBody(r io.Reader, onLine func(string), start time.Time) ([]byte, int64, error) {
	var buf bytes.Buffer
	sc := bufio.NewScanner(io.TeeReader(r, &buf))
	// 单帧可能很大（实测见过 40 万字节的响应），默认 64KB 上限不够。
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var ttfb int64 = -1
	for sc.Scan() {
		if ttfb < 0 {
			ttfb = time.Since(start).Milliseconds()
		}
		if onLine != nil {
			onLine(sc.Text())
		}
	}
	if ttfb < 0 {
		ttfb = time.Since(start).Milliseconds()
	}
	if err := sc.Err(); err != nil {
		return buf.Bytes(), ttfb, err
	}
	return buf.Bytes(), ttfb, nil
}

// reasoningInLine 从单个 wrb.fr 行里取出思考链。
//
// 位置是 inner[4][0][37][0][0]，比正文（inner[4][0][1][0]）深两层。
// 只有 3.1 Pro 会产出，3.6 Flash / Flash-Lite 恒为空。
//
// 时序（实测一次过河谜题）：思考链在头 5 帧里累积到 660 字符，此时正文还是空；
// 从第 6 帧起正文开始增长而思考链冻结不再变。两者都是**累积全文**不是增量，
// 所以流式侧可以直接复用 deltaTracker 的前缀 diff。
func reasoningInLine(line string) string {
	if !strings.Contains(line, `"wrb.fr"`) || len(line) < 200 {
		return ""
	}
	var arr []interface{}
	if err := json.Unmarshal([]byte(line), &arr); err != nil || len(arr) == 0 {
		return ""
	}
	first, ok := arr[0].([]interface{})
	if !ok || len(first) < 3 {
		return ""
	}
	innerStr, ok := first[2].(string)
	if !ok || len(innerStr) < 50 {
		return ""
	}
	var inner []interface{}
	if err := json.Unmarshal([]byte(innerStr), &inner); err != nil || len(inner) <= 4 {
		return ""
	}
	parts, ok := inner[4].([]interface{})
	if !ok || len(parts) == 0 {
		return ""
	}
	p0, ok := parts[0].([]interface{})
	if !ok || len(p0) <= 37 {
		return ""
	}
	lvl1, ok := p0[37].([]interface{})
	if !ok || len(lvl1) == 0 {
		return ""
	}
	lvl2, ok := lvl1[0].([]interface{})
	if !ok || len(lvl2) == 0 {
		return ""
	}
	s, _ := lvl2[0].(string)
	return s
}

// extractReasoning 取整个响应里最长的那段思考链。
// 取最长而不是最后一个：思考链冻结后，后续帧该位置可能缺失或被截短。
//
// 必须跟 extractResponseText 一样过 cleanGeminiText：流式侧 deltaTracker 推的是
// 清洗过的文本，这里不清洗的话两者前缀对不上，收尾补发时会把整段思考链重发一遍
// （实测块顺序变成 R→C→R）。
func extractReasoning(raw string) string {
	best := ""
	for _, line := range strings.Split(raw, "\n") {
		if r := reasoningInLine(line); len(r) > len(best) {
			best = r
		}
	}
	return cleanGeminiText(best)
}

// textsInLine 从单个 wrb.fr 行里取出候选回复文本。
// 上游每帧带的是**累积全文**而不是增量，所以流式转发时要自己做前缀 diff。
func textsInLine(line string) []string {
	if !strings.Contains(line, `"wrb.fr"`) || len(line) < 200 {
		return nil
	}
	var arr []interface{}
	if err := json.Unmarshal([]byte(line), &arr); err != nil || len(arr) == 0 {
		return nil
	}
	first, ok := arr[0].([]interface{})
	if !ok || len(first) < 3 {
		return nil
	}
	innerStr, ok := first[2].(string)
	if !ok || len(innerStr) < 50 {
		return nil
	}
	var inner []interface{}
	if err := json.Unmarshal([]byte(innerStr), &inner); err != nil || len(inner) <= 4 {
		return nil
	}
	parts, ok := inner[4].([]interface{})
	if !ok {
		return nil
	}
	var texts []string
	for _, p := range parts {
		pl, ok := p.([]interface{})
		if !ok || len(pl) < 2 {
			continue
		}
		tl, ok := pl[1].([]interface{})
		if !ok {
			continue
		}
		for _, t := range tl {
			if s, ok := t.(string); ok && s != "" {
				texts = append(texts, s)
			}
		}
	}
	return texts
}

// hasContentFrame 判断响应里到底有没有内容帧。
//
// 故意不复用 extractResponseText：那个会先 cleanGeminiText 掉代码产物，一个纯代码
// 产物的回复在它眼里是空的，但那明明是上游正常出了内容。这里只问"有没有帧"，
// 判断的是链路成没成功，不是内容合不合用。
func hasContentFrame(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		if len(textsInLine(line)) > 0 || reasoningInLine(line) != "" {
			return true
		}
	}
	return false
}

// extractResponseText parses StreamGenerate's wrb.fr stream and returns the
// last non-empty text chunk (matches Python extract_response_text behavior).
func extractResponseText(raw string) string {
	var texts []string
	for _, line := range strings.Split(raw, "\n") {
		texts = append(texts, textsInLine(line)...)
	}
	for i := len(texts) - 1; i >= 0; i-- {
		if strings.TrimSpace(texts[i]) != "" {
			cleaned := cleanGeminiText(texts[i])
			if cleaned == "" {
				// 整条回复都是代码执行产物（问数学/算式时模型直接跑代码），清洗后被剥空。
				// 别返回空——那样 callGemini 会当"无内容帧"报 502，用户看到的是调用失败。
				// 回退：只把 ?code_reference/stdout 标记去掉、保留代码和结果，当普通代码块给。
				cleaned = strings.TrimSpace(codeMarkerRe.ReplaceAllString(texts[i], "```$1"))
			}
			return cleaned
		}
	}
	return ""
}

var codeArtifactRe = regexp.MustCompile("(?s)```(?:python|javascript|text)\\?code_(?:reference|stdout)&code_event_index=\\d+\\n.*?```\\n?")

// codeMarkerRe 只匹配代码产物的**开围栏标记**（```python?code_reference&code_event_index=N），
// 用于清洗后为空时的回退：把标记降级成普通 ```python，保留代码/结果不整条清空。
var codeMarkerRe = regexp.MustCompile("```(python|javascript|text)\\?code_(?:reference|stdout)&code_event_index=\\d+")

func cleanGeminiText(text string) string {
	return strings.TrimSpace(codeArtifactRe.ReplaceAllString(text, ""))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ProbeResult 是 admin 测试接口返回的连通性诊断结果。
type ProbeResult struct {
	OK           bool   `json:"ok"`
	Status       string `json:"status"` // "success" / "blocked_sorry" / "rate_limited" / "upstream_error" / "network_error"
	HTTPCode     int    `json:"http_code"`
	TotalMs      int64  `json:"total_ms"`
	ProxyID      int64  `json:"proxy_id"`
	ProxyName    string `json:"proxy_name"`
	UseDirect    bool   `json:"use_direct"`
	ResponseText string `json:"response_text"` // 截断到 200 字符
	UpstreamSnip string `json:"upstream_snip"` // 上游原始响应前 300 字符
	Diagnostic   string `json:"diagnostic"`    // 中文诊断说明
	Impersonate  string `json:"impersonate"`
}

// probeGemini 直接调 Gemini StreamGenerate（绕过限流），返回详细诊断。
// 不写 db、不消耗限流 slot。
func probeGemini(prompt, proxyURL string) ProbeResult {
	res := ProbeResult{Impersonate: rtCfg().Impersonate}

	inner := make([]interface{}, innerSlots)
	inner[0] = []interface{}{prompt, 0, nil, nil, nil, nil, 0}
	inner[1] = []interface{}{"en"}
	inner[2] = []interface{}{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	inner[6] = []interface{}{0}
	inner[7] = 1
	inner[10] = 1
	inner[11] = 0
	inner[17] = []interface{}{[]interface{}{0}}
	inner[18] = 0
	inner[27] = 1
	inner[30] = []interface{}{4}
	// 抓包里浏览器三种场景（有 cookie / 无 cookie / 扩展思考）全是 [1]。
	// 我们原来写 [2]，是早期抄来的值、协议层已被证伪。含义仍未知，
	// 匿名两个值都能通，但没有理由继续偏离浏览器。
	inner[41] = []interface{}{1}
	inner[53] = 0
	inner[59] = uuid.NewString()
	inner[61] = []interface{}{}
	inner[68] = 1
	probeModel := Models["gemini-3.6-flash"]
	inner[79] = probeModel.Mode

	innerJSON, _ := json.Marshal(inner)
	outer := []interface{}{nil, string(innerJSON)}
	outerJSON, _ := json.Marshal(outer)
	form := url.Values{}
	form.Set("f.req", string(outerJSON))
	body := form.Encode()

	reqid := time.Now().Unix() % 1000000
	// probe 是旁路探测，不回写 cookie 健康度：它的失败原因跟 cookie 无关。
	// 但 at 必须带——否则挂了 cookie 之后连通性探测会一直报 400，假报故障。
	cookieStr, sapisid, authUser := loadCookie()
	// probe 走普通对话载荷，用自动 bl 即可（媒体请求的钉死值见 streamGenerateWithFiles）。
	// endpoint 带上挑中那个号的槽位，否则多账号下探测的是别的账号（见 authuser.go）。
	endpoint := fmt.Sprintf(
		"https://gemini.google.com%s/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?bl=%s&hl=en&_reqid=%d&rt=c",
		geminiURLPrefix(authUser), currentBL(proxyURL), reqid,
	)
	if tok, e := getXSRF(cookieStr, proxyURL, authUser); e == nil && tok != "" {
		form.Set("at", tok)
		body = form.Encode()
	}
	headers := buildGeminiHeaders(cookieStr, sapisid, probeModel.HexID, authUser)

	// 复用主流程同款 client 选择规则:有代理走 stdlib，没代理走 tls-client。
	// 但 probe 需要看 302 的 Location header,所以这里直接发不用 doGeminiRequest。
	var statusCode int
	var raw []byte
	var locHeader string
	var err error

	if proxyURL != "" {
		req, e := http.NewRequest("POST", endpoint, strings.NewReader(body))
		if e != nil {
			res.Status = "network_error"
			res.Diagnostic = "构建请求失败: " + e.Error()
			return res
		}
		applyChromeHeaders(req)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		client := getStdlibClient(proxyURL)
		resp, e := client.Do(req)
		if e != nil {
			res.Status = "network_error"
			res.Diagnostic = "网络层错误（DNS/TCP/TLS 失败）: " + e.Error()
			return res
		}
		defer resp.Body.Close()
		statusCode = resp.StatusCode
		locHeader = resp.Header.Get("Location")
		raw, err = io.ReadAll(resp.Body)
	} else {
		req, e := fhttp.NewRequest("POST", endpoint, strings.NewReader(body))
		if e != nil {
			res.Status = "network_error"
			res.Diagnostic = "构建请求失败: " + e.Error()
			return res
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		client := getTLSClient()
		resp, e := client.Do(req)
		if e != nil {
			res.Status = "network_error"
			res.Diagnostic = "网络层错误（DNS/TCP/TLS 失败）: " + e.Error()
			return res
		}
		defer resp.Body.Close()
		statusCode = resp.StatusCode
		locHeader = resp.Header.Get("Location")
		raw, err = io.ReadAll(resp.Body)
	}
	_ = err
	res.HTTPCode = statusCode
	res.UpstreamSnip = truncate(string(raw), 300)

	switch {
	case statusCode == 302:
		res.Status = "blocked_sorry"
		res.Diagnostic = "IP 被 Google 风控（重定向到 sorry/index）。" +
			"通常 6-24 小时解除，或换 VPN/代理 IP 立即恢复。Location: " + truncate(locHeader, 200)
		return res
	case statusCode == 429:
		res.Status = "rate_limited"
		res.Diagnostic = "Google 直接返回 429 限流。同样是 IP 嫌疑，但风控分支不同（朴素 SDK 路径）。"
		return res
	case statusCode != 200:
		res.Status = "upstream_error"
		res.Diagnostic = fmt.Sprintf("上游返回非 200 (HTTP %d)，可能是协议变更或临时故障。", statusCode)
		return res
	}

	text := extractResponseText(string(raw))
	if text == "" {
		res.Status = "upstream_error"
		res.Diagnostic = "上游 200 但只回了结束帧、没有内容帧，通常是请求被服务端拒绝" +
			"（例如带了不被接受的会话 id 或工具开关），不是帧格式变更。"
		return res
	}

	res.OK = true
	res.Status = "success"
	res.ResponseText = truncate(text, 200)
	res.Diagnostic = "调用成功。延迟 / 内容见上面字段。"
	return res
}

// isTimeoutError 判断错误是不是「等满超时预算」类（区别于连接重置等网络错误）。
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "Client.Timeout") ||
		strings.Contains(msg, "i/o timeout")
}
