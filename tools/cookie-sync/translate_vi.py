"""Build the local Vietnamese extension without embedding credentials."""
import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent
EXT = ROOT / 'ext-src'
STRINGS = {
 '默认账号 /u/0':'Tài khoản mặc định /u/0',
 '扩展弹窗无法读取当前标签页':'Không đọc được tab hiện tại',
 '读取失败: ':'Đọc thất bại: ', '已登录 ✓':'Đã đăng nhập ✓',
 '未登录 / 缺会话 cookie':'Chưa đăng nhập / thiếu cookie phiên',
 ' 个 google cookie':' cookie Google', '已绑定':'Đã liên kết', '未绑定':'Chưa liên kết',
 '请先绑定当前 Gemini 页':'Hãy liên kết tab Gemini trước',
 '读取中…':'Đang đọc…', '读取失败':'Đọc thất bại',
 '已绑定 · 未刷新':'Đã liên kết · chưa tải lại',
 '页面未暴露邮箱':'Không đọc được email từ trang',
 '（与绑定账号不一致）':' (khác tài khoản đã liên kết)',
 '已读取':'Đã đọc', '只读检查':'Kiểm tra', '绑定中…':'Đang liên kết…',
 '无法读取当前页面':'Không đọc được trang hiện tại', '绑定失败':'Liên kết thất bại',
 '绑定当前页':'Liên kết tab', '同步中…':'Đang đồng bộ…',
 '插件状态已回传服务端':'Đã gửi trạng thái đến máy chủ',
 '状态同步失败':'Đồng bộ trạng thái thất bại', '同步插件状态':'Gửi trạng thái',
 '保活中…':'Đang giữ phiên…', '立即保活':'Giữ phiên',
 '入池中…':'Đang thêm…', '立即入池':'Thêm vào pool',
 '页面没有返回身份信息':'Trang không trả về danh tính',
 '检测到悬挂的任务锁（已持有 ':'Phát hiện tác vụ treo (đã giữ khóa ',
 's），强制接管':' giây), tiếp quản',
 'get() 补齐后仍缺:':'Vẫn thiếu cookie sau khi đọc bổ sung:',
 '未绑定 Gemini 页面':'Chưa liên kết tab Gemini',
 '绑定页面已关闭，请重新绑定':'Tab đã đóng; hãy liên kết lại',
 '绑定页面已离开 Gemini，请重新绑定':'Tab không còn ở Gemini; hãy liên kết lại',
 '绑定页面账号槽位已改变（原 ':'Chỉ số tài khoản đã đổi (trước ',
 '，当前 ':', hiện tại ', '），请重新绑定':'); hãy liên kết lại',
 '绑定页面的 Cookie 存储区已改变，请重新绑定':'Kho cookie của tab đã đổi; hãy liên kết lại',
 '弹窗传入的目标页面已关闭，请回到 Gemini 页面重试':'Tab đã đóng; hãy quay lại Gemini và thử lại',
 '没有获取到当前活动标签页，请在 Gemini 页面重新打开扩展弹窗':'Không tìm được tab hiện tại; hãy mở tiện ích từ tab Gemini',
 '当前活动页面不是 Gemini 页面，未绑定（当前地址：':'Tab hiện tại không phải Gemini (địa chỉ: ',
 '不可读取':'không đọc được', '无法读取页面账号（':'Không đọc được tài khoản trên trang (',
 '），请刷新该 Gemini 页后重试':'); hãy tải lại tab Gemini và thử lại',
 '该页面未登录 Gemini（页面没有会话令牌），未绑定':'Trang chưa đăng nhập Gemini (thiếu token phiên); chưa liên kết',
 '页面未暴露账号邮箱，无法确认是哪个账号，未绑定':'Không xác nhận được email tài khoản; chưa liên kết',
 '页面地址是 ':'Địa chỉ trang là ', '，但页面会话是 ':', nhưng phiên trên trang là ',
 '，请在该账号自己的页面上绑定':'; hãy liên kết ở đúng trang của tài khoản',
 '已绑定 ':'Đã liên kết ', '，由页面识别':', nhận diện từ trang',
 '已解除页面绑定':'Đã hủy liên kết tab', '绑定页显示未登录':'Tab liên kết chưa đăng nhập',
 '绑定页账号已变为 ':'Tài khoản trên tab đã đổi thành ', '（绑定时是 ':' (khi liên kết là ',
 '已读取绑定页，不刷新；':'Đã đọc tab liên kết, không tải lại; ',
 '刷新失败: ':'Tải lại thất bại: ',
 '刷新后页面账号槽位改变，停止自动处理':'Chỉ số tài khoản đổi sau khi tải lại; dừng tự động',
 '刷新后页面账号槽位改变，请重新绑定':'Chỉ số tài khoản đổi sau khi tải lại; hãy liên kết lại',
 '刷新绑定页面':'Tải lại tab liên kết',
 '（被风控重定向到 sorry 页，本轮只提取 cookie）':' (chuyển đến trang kiểm tra Google; chỉ đọc cookie)',
 '[sorry 页]':'[trang kiểm tra Google]', '扩展已停用':'Tiện ích đã tắt',
 '上一轮任务仍在进行':'Tác vụ trước vẫn đang chạy',
 '没有可用的 gemini 标签页':'Không có tab Gemini khả dụng',
 '已刷新页面':'Đã tải lại trang', '冷却中（剩余 ':'Đang chờ (còn ',
 's，不刷新）':' giây; không tải lại)', '会话有效':'Phiên hợp lệ',
 '未检测到完整登录 cookie':'Chưa tìm thấy đủ cookie đăng nhập',
 'rotate cookies failed (忽略)':'Không làm mới được cookie (bỏ qua)',
 '未绑定 Gemini 页面，进入休眠':'Chưa liên kết Gemini; tạm nghỉ',
 '冷却窗口内仍未取到登录 cookie':'Hết thời gian chờ nhưng chưa đọc được cookie đăng nhập',
 '冷却中，跳过刷新，直接提取 cookie（剩余 ':'Đang chờ, đọc cookie và bỏ qua tải lại (còn ',
 '没有读到 google cookie':'Không đọc được cookie Google',
 '未登录（缺少 SID/SAPISID/__Secure-1PSID），跳过入池':'Chưa đăng nhập (thiếu SID/SAPISID/__Secure-1PSID); bỏ qua thêm vào pool',
 '绑定页面已改变，本轮未入池；请重新绑定当前页':'Tab liên kết đã đổi; chưa thêm vào pool. Hãy liên kết lại',
 '绑定记录缺少账号邮箱（旧版绑定），请重新点「绑定当前页」':'Liên kết cũ thiếu email; hãy bấm Liên kết tab lại',
 '读不到绑定页的账号（':'Không đọc được tài khoản trên tab liên kết (',
 '），本轮未入池':'); chưa thêm vào pool',
 '，本轮未入池；请重新绑定':'; chưa thêm vào pool. Hãy liên kết lại',
 '服务端返回 ':'Máy chủ trả về ', '控制器返回 ':'Bộ điều khiển trả về ',
 '已入池':'Đã thêm vào pool', ' · 校验未通过：':' · Kiểm tra chưa đạt: ',
 '推送失败: ':'Gửi thất bại: ', '（目标 ':' (đích ',
 '扩展状态心跳':'Trạng thái tiện ích định kỳ', '定时状态心跳':'Báo trạng thái định kỳ',
 '扩展启动状态':'Trạng thái khi khởi động', '用户手动同步状态':'Gửi trạng thái thủ công',
 '（':' (', '）':')', '；':'; ',
}

for name in ['popup.js', 'background.js']:
    p = EXT / name
    text = p.read_text(encoding='utf-8')
    for source, target in sorted(STRINGS.items(), key=lambda item: -len(item[0])):
        text = text.replace(source, target)
    if name == 'background.js':
        # This deployment uses only the popup, never externally controlled pages.
        marker = 'chrome.runtime.onMessageExternal.addListener'
        if marker in text:
            text = text[:text.index(marker)]
        text = text.replace("controller: 'http://127.0.0.1:9280'", "controller: 'http://localhost:8083'")
        text = text.replace("pushMode: 'controller'", "pushMode: 'service'")
        text = text.replace("profile: '',", "profile: 'laptop',", 1)
    else:
        text = text.replace("|| 'http://127.0.0.1:9280'", "|| 'http://localhost:8083'")
    p.write_text(text, encoding='utf-8')

HTML = {
 '登录状态':'Đăng nhập', 'Cookie 数':'Số cookie', '上次保活':'Giữ phiên lần cuối',
 '上次入池':'Đồng bộ lần cuối', '入池结果':'Kết quả đồng bộ',
 '绑定页面':'Tab liên kết', '绑定账号':'Tài khoản liên kết', '账号槽位':'Chỉ số tài khoản',
 'Cookie 存储区':'Kho cookie', '页面地址':'Địa chỉ tab', '页面账号':'Email trên tab',
 '页面读取':'Kiểm tra tab', '绑定当前页':'Liên kết tab', '只读检查':'Kiểm tra',
 '解除绑定':'Hủy liên kết', '同步插件状态':'Gửi trạng thái', '未绑定':'Chưa liên kết',
 '在要入池的那个账号的 Gemini 页面点「绑定当前页」，账号以页面实际登录的邮箱为准。不扫描其他账号，不刷新或新建页面。':
 'Mở tab Gemini của tài khoản cần thêm, rồi bấm “Liên kết tab”. Tiện ích xác nhận email trên tab; không quét tài khoản khác hoặc tự mở tab mới.',
 '摘要指纹':'Mã nhận diện cookie', 'Profile 标识':'Tên hồ sơ', '本机浏览器':'laptop',
 '推送模式':'Chế độ gửi', '服务端（远程服务器，推荐）':'Máy chủ từ xa (khuyên dùng)',
 '控制器（同机/同网 Chromium）':'Bộ điều khiển Chromium nội bộ', '目标地址':'Địa chỉ máy chủ',
 'http://服务器IP:8083':'http://localhost:8083',
 '密钥（服务端模式填 API Key；控制器模式填共享密钥，可空）':'API key (lưu riêng trên trình duyệt)',
 '启用扩展（自动保活/入池）':'Bật tiện ích (giữ phiên và đồng bộ)', '自动保活':'Tự động giữ phiên',
 '保活间隔（分钟）':'Giữ phiên mỗi (phút)', '入池间隔（分钟）':'Đồng bộ mỗi (phút)',
 '刷新冷却（秒，防风控）':'Chờ giữa các lần tải lại (giây)',
 '保存':'Lưu', '立即保活':'Giữ phiên', '立即入池':'Thêm vào pool',
 '「服务端」模式直推 gemini-web2api（端点 /api/browser/ingest，用 API Key 鉴权），适合浏览器在你电脑、服务在远端服务器；「控制器」模式推给同机/同网的 Chromium 控制器。抓取优先只读，会话失效时才刷新页面。':
 'Cookie chỉ được gửi đến VPS Tailscale đã cấu hình. Chọn “Máy chủ từ xa”, nhập API key rồi lưu. Mỗi tiện ích liên kết một tab và một tài khoản; giữ tab Gemini mở để tự động đồng bộ.',
}
p = EXT / 'popup.html'
text = p.read_text(encoding='utf-8')
for source,target in sorted(HTML.items(), key=lambda item:-len(item[0])):
    text=text.replace(source,target)
text=text.replace('lang="zh-CN"','lang="vi"').replace('Gemini Cookie Sync','Gemini — Đồng bộ phiên')
text=text.replace('width:340px','width:400px').replace('input[type=text], input[type=number]', 'input[type=text], input[type=password], input[type=number]')
text=text.replace('type="text" id="token"','type="password" id="token" autocomplete="off"')
p.write_text(text,encoding='utf-8')

p=EXT/'manifest.json'
manifest=json.loads(p.read_text(encoding='utf-8'))
manifest['name']='Gemini — Đồng bộ phiên (Tiếng Việt)'
manifest['version']='1.1.7.1'
manifest['description']='Liên kết tab Gemini và đồng bộ phiên đăng nhập đến VPS Tailscale của bạn.'
manifest['action']['default_title']='Gemini — Đồng bộ phiên'
manifest['host_permissions']=['https://*.google.com/*','http://localhost/*','http://127.0.0.1/*','http://*/*','https://*/*']
manifest.pop('externally_connectable',None)
p.write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')

# The VPS download endpoint must serve the same verified Vietnamese build.
embedded=ROOT.parent.parent/'internal'/'app'/'ext_assets'
for name in ['manifest.json','background.js','popup.js','popup.html']:
    embedded.joinpath(name).write_bytes(EXT.joinpath(name).read_bytes())
print('Vietnamese extension prepared; no API key embedded.')
