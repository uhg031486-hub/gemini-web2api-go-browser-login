# Pool tài khoản và quản trị tiếng Việt

Bản `4.22.0-browser-login.6-server-maintenance` cùng extension `1.2.1` lưu nhiều
liên kết và mặc định giao phiên cho VPS tự duy trì. Bản trước đã sửa lỗi thêm tài khoản Google khác nhưng
ghi đè dòng có cùng hồ sơ extension và cùng `/u/0`.

Máy chủ xác minh email bằng phiên Google trước khi nhập, sau đó dùng email đó
làm định danh ổn định. Cùng tài khoản được cập nhật tại dòng cũ khi đổi hồ sơ
hoặc đổi số thứ tự `/u/N`; tài khoản khác được thêm thành dòng riêng. Việc cập
nhật diễn ra trong transaction và được khóa để tránh hai lần đồng bộ tạo dòng
trùng. Các dòng cũ không xác định được danh tính được giữ nguyên.

Để thêm nhiều tài khoản:

1. Mở Gemini của tài khoản muốn thêm, bấm **Liên kết tab** trong extension.
2. Kiểm tra email, rồi bấm **Thêm vào pool**. Khi nhận thông báo đã giao phiên
   cho VPS, đóng tab Gemini đó, không bấm Đăng xuất Google.
3. Lặp lại với tài khoản khác. Với các phiên độc lập, nên dùng hồ sơ Chrome
   riêng; các cửa sổ ẩn danh cùng một hồ sơ thường dùng chung kho cookie.

Extension giữ danh sách liên kết theo email đã xác minh, không thay liên kết
cũ khi thêm tài khoản khác. Có thể bấm **Liên kết các tab Gemini đang mở** rồi
**Đồng bộ các tài khoản**. Chế độ mặc định `maintenanceMode=server` không tự
tải lại tab hay gửi cookie nền; các nút/lịch giữ phiên trên trình duyệt bị vô
hiệu hóa để tránh hai bên cùng xoay cookie. Máy chủ xác minh email rồi lưu
nguồn `manual` cho phiên để bộ gia hạn sẵn có xử lý. API nhận
`maintenance_mode=server`; client cũ không gửi trường này vẫn giữ chế độ
trình duyệt như trước. Cùng email được cập nhật tại dòng cũ khi đổi chế độ.

Đăng xuất Google có thể làm phiên đã
lưu mất hiệu lực; giữ một dòng trong pool không thể làm sống lại phiên đã bị
Google thu hồi. Muốn dùng lại phiên hết hiệu lực cần đăng nhập và đồng bộ lại.

Cookie là dữ liệu nhỏ; bộ nhớ chủ yếu do trình duyệt và trang Gemini sử dụng.
Đã xác minh phiên Firefox số 7 gọi được 4 Hydrogen và được lịch VPS tự gia hạn
khi Firefox đóng hoàn toàn. Đây vẫn là cookie, không phải refresh JWT như Kimi.
Cookie Chrome có thể bị ràng buộc thiết bị: giao quyền cho VPS không bảo đảm
gia hạn thành công; cần xuất phiên mới và thử thực tế. Các phiên đã hết hiệu
lực cần đăng nhập lại, không thể khôi phục chỉ bằng đổi chế độ.
Trạng thái thành công quá 30 phút được ghi
là cần kiểm tra; lỗi thiếu phiên được ghi là cần đồng bộ, không kết luận bị khóa.

Trong đợt sửa này, tài khoản hiện tại được xác minh và giữ nguyên. Phiên tài
khoản trước đó được tìm thấy trong bản sao database, nhưng không vượt qua bước
xác minh Google nên không được ép nhập trở lại.

Trang `/admin/` mặc định tiếng Việt, có thể đổi sang tiếng Anh bằng nút EN.
Thông tin đăng nhập quản trị đã đổi theo yêu cầu riêng của người dùng; không
được nhúng mật khẩu hay API key vào mã nguồn hoặc gói phát hành.

9Router có nhà cung cấp **Gemini Web**, tiền tố **geminiweb**. Ví dụ tên model:
`geminiweb/gemini-4-neon-high`. Chat đã được gọi thật qua router. Thử nghiệm
gọi công cụ trực tiếp thành công, nhưng thử nghiệm qua router chưa đạt ổn định;
không coi đó là xác nhận Cline hoàn chỉnh.
