# Hướng dẫn cài đặt và sử dụng nhanh (Tiếng Việt)

Tài liệu này hướng dẫn từng bước để tải về, chạy máy chủ **gemini-web2api-go** và cài **Extension Firefox / Chromium** để đồng bộ nhiều tài khoản Google Gemini vào pool dùng chung qua chuẩn OpenAI API.

---

## Bước 1: Tải và chạy máy chủ (`gemini-web2api`)

### Cách A — Tải file chạy sẵn (Khuyên dùng, không cần cài Go/Docker)
1. Vào mục **[Releases](https://github.com/uhg031486-hub/gemini-web2api-go-browser-login/releases)** của repo này.
2. Tải file phù hợp với hệ điều hành của bạn:
   - **Windows 64-bit**: `gemini-web2api-go_v..._windows_amd64.exe`
   - **Linux / VPS ARM64**: `gemini-web2api-go_v..._linux_arm64` hoặc `linux_amd64`
   - **macOS Apple Silicon**: `gemini-web2api-go_v..._darwin_arm64`
3. Chạy máy chủ trong terminal / CMD:

**Trên Windows (CMD / PowerShell):**
```powershell
mkdir data
.\gemini-web2api-go_*.exe --port 8083 --host 0.0.0.0 --admin-token mat-khau-admin-cua-ban
```

**Trên Linux / macOS:**
```bash
chmod +x gemini-web2api-go_*
./gemini-web2api-go_* --port 8083 --host 0.0.0.0 --admin-token mat-khau-admin-cua-ban
```

### Cách B — Chạy bằng Docker Compose
```bash
git clone https://github.com/uhg031486-hub/gemini-web2api-go-browser-login
cd gemini-web2api-go-browser-login
ADMIN_TOKEN=mat-khau-admin-cua-ban docker compose up -d --build
```

### Cách C — Build trực tiếp từ source (cần Go 1.26+)
```bash
git clone https://github.com/uhg031486-hub/gemini-web2api-go-browser-login
cd gemini-web2api-go-browser-login
go build -o gemini-web2api.exe .
.\gemini-web2api.exe --port 8083 --host 0.0.0.0 --admin-token mat-khau-admin-cua-ban
```

---

## Bước 2: Lấy API Key trong trang Quản trị

1. Mở trình duyệt vào **`http://localhost:8083/admin`** (hoặc thay `localhost` bằng IP VPS của bạn).
2. Nhập `admin-token` bạn vừa đặt ở Bước 1 để đăng nhập trang quản trị.
3. Vào tab **Tổng quan / Overview** (hoặc **Cài đặt**) để copy **API Key** dạng `sk-gemini-...`.

*(Tùy chọn khuyến nghị trong tab **Cài đặt**)*:
- Nếu chạy dịch đa luồng từ 10–25 luồng đồng thời, có thể đặt **`per_ip_concurrent = 0`**, **`per_ip_rpm = 0`**, **`per_ip_rph = 0`** (khi dùng pool nhiều tài khoản hoặc mạng tin cậy), bật **`temporary_chat = true`** để không làm rác lịch sử chat trên web Google, giữ **`max_prompt_bytes = 128000`**.

---

## Bước 3: Cài đặt Extension đồng bộ tài khoản Google

### Dành cho Firefox *(Khuyên dùng — ổn định nhất cho multi-account)*
- **Nguồn code trong repo**: thư mục [`tools/cookie-sync/firefox-ext/`](tools/cookie-sync/firefox-ext/) (hoặc tải file `gw2a-cookie-sync-firefox-ext.zip` ở Releases rồi giải nén).
- **Cách cài trên Firefox thường**:
  1. Mở tab mới, truy cập `about:debugging#/runtime/this-firefox`.
  2. Bấm **Load Temporary Add-on...** (Tải tiện ích tạm thời...).
  3. Chọn file `manifest.json` trong thư mục `firefox-ext`.
  *(Mẹo: nếu dùng nhiều tài khoản Google tách biệt trên cùng 1 Firefox, nên cài **Firefox Multi-Account Containers** hoặc mở nhiều **Firefox Profile** qua `about:profiles` — mỗi profile Firefox đăng nhập 1 tài khoản Google riêng và đều cài extension này, rất gọn và không đá session của nhau.)*

### Dành cho Chrome / Edge / Brave
- Thư mục [`tools/cookie-sync/ext-src/`](tools/cookie-sync/ext-src/) (hoặc tải `gw2a-cookie-sync-extension.zip` ở Releases rồi giải nén).
- Mở `chrome://extensions` (hoặc `edge://extensions`), bật **Developer mode**, bấm **Load unpacked** và chọn thư mục vừa giải nén.

---

## Bước 4: Nạp nhiều tài khoản Google vào Pool

1. Mở `https://gemini.google.com` và đăng nhập tài khoản Google của bạn.
2. Bấm vào biểu tượng **Extension Gemini — Đồng bộ phiên**:
   - **Địa chỉ máy chủ**: giữ mặc định `http://localhost:8083` (hoặc điền `http://<IP-VPS>:8083` nếu bạn đặt server trên VPS).
   - **API key**: dán `sk-gemini-...` vừa copy ở Bước 2 -> bấm **Lưu**.
   - Chọn chế độ **Máy chủ từ xa (khuyên dùng)** (mặc định).
3. Bấm **Liên kết tab** -> kiểm tra hiện đúng email Google -> bấm **Thêm vào pool**.
4. Khi thấy báo thành công, lặp lại với các tài khoản Google khác (trên profile/container khác). **Không bấm Đăng xuất Google**, chỉ cần đóng tab là được — server sẽ tự xoay và duy trì phiên.

---

## Bước 5: Kết nối Client / App dịch thuật / Coding Agent

Cấu hình bất kỳ app nào hỗ trợ chuẩn **OpenAI Chat Completions**:
- **Base URL**: `http://localhost:8083/v1` (hoặc `http://<IP-VPS>:8083/v1`)
- **API Key**: `sk-gemini-...`
- **Các model khuyên dùng**:
  - **`gemini-4-hydrogen-low`**: Nhanh, hợp dịch thuật đa luồng, tóm tắt, chat thường.
  - **`gemini-4-neon-medium`** / **`gemini-4-neon-high`**: Suy luận tốt, gọi `tool_calls` chuẩn cho coding agent.
  - Xem thêm chi tiết trong [MODEL-LEVELS-VI.md](MODEL-LEVELS-VI.md) và [POOL-VA-QUAN-TRI-VI.md](POOL-VA-QUAN-TRI-VI.md).

### Lưu ý về giới hạn kỹ thuật của bản Web
- **Cửa sổ ngữ cảnh thực tế**: ~128KB UTF-8 cho prompt gửi trực tiếp (vượt 128KB proxy tự chuyển thành file đính kèm `message.txt`, model web đọc được tối đa khoảng ~160KB tức ~30k–40k token). Với file truyện dài hàng trăm KB – nhiều MB, cần chia từng đoạn/chương (5–30KB/luồng).
- **Tốc độ khuyến nghị**: Chạy khoảng **1 luồng / 1 tài khoản** (ví dụ 11 acc chạy 10–11 luồng) cho độ ổn định cao nhất nếu treo dịch liên tục nhiều giờ.
