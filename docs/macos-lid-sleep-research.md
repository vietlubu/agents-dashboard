# macOS: giữ máy chạy khi gập nắp, vẫn cho phép Sleep chủ động

## Kết luận

Phương án có bằng chứng cụ thể cho dashboard là **tách chặn sleep tự động khỏi chặn sleep do gập nắp**:

- System/display: giữ `caffeinate -i`/`-d` hoặc các public IOPM idle assertions tương đương.
- Gập nắp: gọi RootDomain user-client selector **`kPMSetClamshellSleepState = 12`** với một scalar `uint64_t`: **1 đặt cờ chặn sleep do gập nắp, 0 bỏ cờ đó**.
- Không dùng `pmset disablesleep 1`, `caffeinate -s`, `PreventSystemSleep`/`InternalPreventSleep`, hoặc sao chép các selector 9/15 của Sleep Control Center vào luồng bình thường.

Đây là cơ chế thực tế, không phải phỏng đoán từ tên string: bản Sleep Control Center đã cài có đúng lời gọi selector 12; mã nguồn của tác giả Amphetamine cũng cung cấp triển khai tương ứng. Tuy nhiên, đây là low-level/private behavior, **không phải hợp đồng API public được Apple cam kết ổn định cho ứng dụng bên thứ ba**.

**Giới hạn không thể giấu:** selector này thay một bit dùng chung với `powerd`, không có lease theo process. Không thể hứa ownership độc lập, tự dọn sau mọi crash, hoặc không bao giờ sleep khi đổi nguồn lúc nắp đóng. Một watchdog chỉ giảm rủi ro; không biến nó thành assertion do kernel quản lý theo process.

## Mức độ chứng minh

| Bằng chứng | Đã thực hiện | Chứng minh được | Không chứng minh được |
|---|---|---|---|
| Đọc `sw_vers`/`uname -v` | macOS 14.5, build 23F79, kernel `xnu-10063.121.3~5`, ARM64 | Chọn đúng phiên bản XNU để phân tích | Hành vi của macOS tương lai |
| Phân tích tĩnh app đã cài | Sleep Control Center 2.27, build 180 | Lời gọi và các đường code mô tả bên dưới | Hiệu quả trên phần cứng, pin/sạc, click menu Apple |
| Đọc XNU đúng tag | `xnu-10063.121.3`, commit `2c2f96dc…` | Dispatch, quyền, mask, đường lid và software sleep | Caller trong giao diện menu Apple vốn không nằm trong source XNU |
| Đọc PowerManagement | tag `PowerManagement-1754.120.2`, commit `8531a6e…` | Corroboration về writer `powerd` và phân loại assertions | Chưa có manifest Apple xác nhận tarball này là build trong 23F79 |
| Compile-only với SDK cài sẵn | `clang -fsyntax-only -Wall -Wextra -Werror`, exit 0 | Hằng selector 12 và chữ ký transport C hợp lệ | Kernel chấp nhận lời gọi hoặc hiệu quả thực tế |

Không chạy app, gọi IOKit, chạy lệnh power-management, tạo assertion thật, đóng nắp, xin quyền, hoặc thao tác UI. Không có runtime/hardware verification mới.

## 1. Contract và quyền chính xác

Trong XNU khớp phiên bản đang dùng:

- Dispatch selector 12 nhận **một scalar**, không có structure hoặc output; `checkEntitlement = NULL` [S1].
- Handler chuyển scalar khác 0 thành true rồi gọi `setClamShellSleepDisable(..., kClamshellSleepDisablePowerd)`; không có `clientHasPrivilege` trong handler đó [S2].
- User-client đặt `kIOUserClientEntitlementsKey = false` khi khởi tạo [S3].
- Bit được thay đổi là **`kClamshellSleepDisablePowerd = 0x02`**, không phải `SleepDisabled` [S4].

Vì vậy đường code này không có bước AppleScript/admin password và không cần sudo helper trong thiết kế. Vẫn phải kiểm tra lỗi mở connection/IOReturn: source không bảo đảm sandbox, signing, OS hoặc hardware khác luôn cho mở user-client.

Đoạn contract sau đã **compile-only**, không link thành executable hoặc gọi kernel:

```c
#include <stdbool.h>
#include <stdint.h>
#include <IOKit/IOKitLib.h>
#include <IOKit/pwr_mgt/IOPMLib.h>
#include <IOKit/pwr_mgt/IOPMLibDefs.h>

_Static_assert(kPMSetClamshellSleepState == 12, "unexpected selector");

IOReturn checked_lid_request(bool disable) {
    io_connect_t connection = IOPMFindPowerManagement(MACH_PORT_NULL);
    if (connection == MACH_PORT_NULL) return kIOReturnNotReady;
    uint64_t input = disable ? 1 : 0;
    IOReturn result = IOConnectCallScalarMethod(
        connection, kPMSetClamshellSleepState, &input, 1, NULL, NULL);
    IOServiceClose(connection);
    return result;
}
```

Đây chỉ là transport. Return success không cung cấp ownership token và không có output chứng minh trạng thái áp dụng lâu dài.

## 2. Vì sao khác `pmset disablesleep`

`shouldSleepOnClamshellClosed()` kiểm tra:

```text
!clamshellDisabled
&& !(desktopMode && acAdaptorConnected)
&& !clamshellSleepDisableMask
```

Mask khác 0 khiến sự kiện gập nắp không tạo yêu cầu clamshell sleep. Predicate này không giới hạn mask chỉ có tác dụng trên AC [S5]. **Theo source**, nó áp dụng cho cả pin và sạc; không đồng nghĩa đã thử phần cứng.

Lệnh sleep phần mềm đi qua `sleepSystemOptions()` → `privateSleepSystem(kIOPMSleepReasonSoftware)` → demand-sleep transition, không kiểm tra predicate gập nắp trên [S6]. Public idle assertion không phải CPU/demand-sleep veto [S7]. Vì vậy tổ hợp **selector 12 + idle-only assertions** giữ nguyên đường sleep chủ động trong kiến trúc source.

Ngược lại `SleepDisabled` đặt `userDisabledAllSleep`, và kernel kiểm tra nó cho cả idle lẫn demand sleep [S8]. Bỏ yêu cầu password bằng sudoers không sửa được việc menu Apple bị chặn.

Không suy diễn rằng đã quan sát click **Apple → Sleep**. Phần UI caller không có trong XNU source và lượt nghiên cứu này không thực hiện thao tác đó.

## 3. Lifecycle, xung đột và nguồn điện

### Trạng thái dùng chung, không phải assertion theo PID

- Selector 12 luôn thay bit `0x02` của powerd; không nhận PID/client ownership [S2, S4].
- `clientClose()` chỉ terminate, `stop()` chỉ giải phóng task. Chúng không gửi false [S9]. Giữ connection mở cũng không tạo lease.
- `AppleClamshellCausesSleep` là **effective status được publish**, không phải control hoặc getter của bit mà riêng app sở hữu [S10]. Không dùng nó để suy ra một baseline có thể khôi phục an toàn cho mọi writer.
- `powerd` có writer riêng gửi selector này với 0/1; một writer khác có thể ghi đè ý định của app [S11].

Do đó không tự reset flag khi startup và không gọi reset chỉ vì đọc thấy effective lid sleep bị tắt. Điều đó có thể thay đổi trạng thái do app khác/system tạo ra.

### Đổi pin/sạc khi nắp đóng

XNU đúng phiên bản cập nhật trạng thái AC rồi reevaluate lid. **Không thấy AC handler trực tiếp xóa mask** [S12]; không nên ghi rằng kernel luôn reset selector khi cắm/rút sạc. Source `powerd` chứng minh một writer dùng chung khác tồn tại, nhưng chưa đủ để kết luận nguyên nhân của mọi AC-transition failure.

Sleep Control Center và tác giả Amphetamine đều cảnh báo đổi nguồn khi nắp đóng có thể gây sleep [S13, S14]. Reapply sau wake/power-source notification có thể giúp phục hồi, nhưng không bảo đảm thắng race trước khi sleep đã được yêu cầu. **Không dùng global disablesleep làm fallback âm thầm.**

### An toàn nhiệt/pin

Đường sleep do over-temperature và power emergency/low battery độc lập với predicate gập nắp [S15]. Giữ nguyên các bảo vệ này; không thêm forced-sleep veto hoặc cố đánh thức máy để vượt qua chúng.

## 4. App khác thực sự làm gì

| Triển khai | Cơ chế có bằng chứng | Kết luận |
|---|---|---|
| Sleep Control Center 2.27 đã cài | `LidMonitor -setClamShellSleepDisabled:` tại ARM64 `0x1000071a0`: `IOPMFindPowerManagement` → `IOConnectCallMethod(..., 12, BOOL, ...)` → `IOServiceClose` | Có đường lid-only riêng, không phải `pmset disablesleep` trong setter này. Chưa giải được toàn bộ dynamic caller/quit/reapply chain của binary stripped. |
| Amphetamine author CDMManager/Enhancer | Source dùng selector 12; utility recovery gửi false. LaunchAgent watchdog kiểm tra owner và chạy recovery [S16] | Củng cố cơ chế và nhu cầu cleanup bên ngoài kernel, không chứng minh automatic PID cleanup. |
| Amphetamine Power Protect | `sudo pmset -a disablesleep 1/0` cùng sudoers cho hai lệnh [S14, S17] | Giảm password sau administrative install, nhưng vẫn global sleep veto; không đáp ứng yêu cầu menu Apple. |
| CoffeeTea | Bridge selector 12, idle/display assertions riêng, cleanup quit/signal và startup reset [S18] | Triển khai readable để đối chiếu. Startup reset không ownership-safe; signal cleanup không xử lý SIGKILL. Không lặp lại README gọi đây là Apple-supported public API. |

Sleep Control Center còn có `updateUserAssertionLevels` dùng selector **9** và routine lân cận dùng **15**; XNU có administrator check cho các đường này, khác selector 12 [S19]. Không cần sao chép chúng cho thiết kế password-free. Getter/setter `isClamshellSleepDisabled`/`setClamshellSleepDisabled:` trong binary chỉ đọc/ghi ivar, khác actual kernel setter viết `setClamShellSleepDisabled:`.

App's own Sleep action đã trace là `IOPMSleepSystem` không có pre-release trong call chain đã xem. Việc này không chứng minh cả asynchronous callbacks hoặc menu Apple có hành vi giống nhau.

`AppliesOnLidClose` assertion modifier là hướng khác nhưng yêu cầu Apple-private entitlement `com.apple.private.iokit.assertonlidclose`; không phải public per-process replacement khả dụng cho dashboard [S20].

## 5. Thiết kế đề xuất cho dashboard

**Chốt cơ chế:** public idle/display assertions + optional native selector-12 lid control. Giữ ba policy `off`/`agent`/`always` hiện có; chỉ thay cách áp scope lid trên macOS.

Yêu cầu triển khai:

1. Gọi transport native từ macOS inhibitor; không shell admin, không `disablesleep 1`, không mạnh hóa idle assertion thành demand-sleep veto. Linux/Windows không thay chính sách.
2. Tách **desired lid**, **request thành công**, **effective policy quan sát được** và lỗi. Không báo `Held.Lid` chỉ từ bool đã lưu; `AppleClamshellCausesSleep` không phải ownership proof.
3. Chỉ enable khi policy đang yêu cầu inhibition và scope lid được chọn. Scope/policy off, kết thúc agent/grace, app's Sleep now và normal Quit phải thực hiện cleanup đã áp dụng, kiểm tra lỗi. Bỏ mask khi nắp đang đóng có thể lập tức cho phép sleep: đó là hệ quả phải ghi rõ [S21].
4. Không tự thay power policy khi startup; không silent fallback khi API lỗi. Nếu còn `SleepDisabled` do bản cũ, giữ explicit legacy recovery riêng; thao tác đó có thể cần admin một lần, không phải mỗi lần đổi mode.
5. Quan sát wake/power/lid policy để reconciliation best-effort. Tôn trọng forced/manual sleep, thermal và low battery; không hứa không gián đoạn khi đổi nguồn với nắp đóng.
6. Nếu muốn giảm flag còn sót sau parent crash, cần một guardian/watchdog **không privileged** sống độc lập và gửi false khi phát hiện parent mất. Đây là lựa chọn hardening riêng: không giải được simultaneous kill, system/powerd writer conflict hoặc khôi phục ownership không tồn tại. Không tuyên bố chỉ `defer`/`IOServiceClose` là đủ.
7. Công bố đây là shared/private best-effort control và tránh chạy đồng thời nhiều app quản lý gập nắp. Không thể cam kết per-app isolation bằng selector này.

### Verification cần có khi triển khai

Không tác động OS: kiểm chứng adapter bằng transport giả cho selector/input/count/IOReturn; behavioral regression cho policy/scope transitions, release lỗi, normal shutdown và event ordering. Compile bridge trên SDK mục tiêu. Đây không phải proof hardware.

Chỉ nếu người dùng cho phép sau này: kiểm chứng trên pin và AC ổn định, đóng/mở nắp, Apple-menu Sleep, quit/crash và AC transition bằng app fixture. Lượt nghiên cứu này **không thực hiện hoặc yêu cầu tự động** các thao tác đó.

Implementation của dashboard chưa đổi trong lượt nghiên cứu này. Nếu yêu cầu bao gồm per-process ownership, không xung đột và crash cleanup tuyệt đối, chưa có phương án được nguồn đã xem chứng minh đáp ứng tất cả; không nên hứa có.

## Nguồn

- [S1] [XNU Sonoma: selector dispatch](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/RootDomainUserClient.cpp#L347-L354)
- [S2] [XNU Sonoma: selector 12 handler](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/RootDomainUserClient.cpp#L466-L469)
- [S3] [XNU Sonoma: user-client entitlement setup](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/RootDomainUserClient.cpp#L75-L82)
- [S4] [XNU Sonoma: clamshell mask bits](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/IOKit/pwr_mgt/RootDomain.h#L780-L785)
- [S5] [XNU Sonoma: lid predicate](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/IOPMrootDomain.cpp#L4280-L4289)
- [S6] [XNU Sonoma: software-sleep path](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/IOPMrootDomain.cpp#L2732-L2781) / [demand transition](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/IOPMrootDomain.cpp#L8701-L8715)
- [S7] [Apple PowerManagement: idle assertion effect](https://github.com/apple-oss-distributions/PowerManagement/blob/8531a6e0a517bc79fb2fe268506a568a9390f262/pmconfigd/PMAssertions.c#L6503-L6512) / [CPU-demand assertion mapping](https://github.com/apple-oss-distributions/PowerManagement/blob/8531a6e0a517bc79fb2fe268506a568a9390f262/pmconfigd/PMAssertions.c#L5348-L5375)
- [S8] [XNU Sonoma: all-sleep veto](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/IOPMrootDomain.cpp#L7454-L7465)
- [S9] [XNU Sonoma: close/stop do not clear lid bit](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/RootDomainUserClient.cpp#L225-L241)
- [S10] [XNU Sonoma: effective property publication](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/IOPMrootDomain.cpp#L4318-L4326)
- [S11] [Apple PowerManagement: shared-selector writer](https://github.com/apple-oss-distributions/PowerManagement/blob/8531a6e0a517bc79fb2fe268506a568a9390f262/pmconfigd/PMAssertions.c#L1371-L1448)
- [S12] [XNU Sonoma: AC state and reevaluation](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/IOPMrootDomain.cpp#L8446-L8462)
- [S13] [Sleep Control Center: official lid help and AC warning](https://prevent-mac-from-sleeping.3bitlab.com/prevent_mac_sleep_help.html)
- [S14] [Amphetamine author: Power Protect rationale/installation](https://github.com/x74353/Amphetamine-Power-Protect/blob/4937590499c5504f9623dcf290c099349cf8e8fd/README.md)
- [S15] [XNU Sonoma: thermal/low-power sleep](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/IOPMrootDomain.cpp#L8331-L8358)
- [S16] [Amphetamine author: selector-12 recovery](https://github.com/x74353/Amphetamine-Enhancer/blob/20b6f4c5452bea55c7de8e57f5baa0cdd4b39d8c/Xcode%20Project/CDMManager/CDMManager/AppDelegate.m) / [watchdog script](https://github.com/x74353/Amphetamine-Enhancer/blob/20b6f4c5452bea55c7de8e57f5baa0cdd4b39d8c/Xcode%20Project/Amphetamine%20Enhancer/Scripts/amphetamine-enhancer-cdmManager.sh)
- [S17] [Amphetamine author: fixed sudoers commands](https://github.com/x74353/Amphetamine/blob/84740c43c66ee9fae9e6f668a7c129e84e9fd352/Files/amphetamine_PowerProtect)
- [S18] [CoffeeTea: IOKit bridge](https://github.com/chenjh16/CoffeeTea/blob/1c167ea573ed907885c1963e3082e5c4152bd8db/CoffeeTea/Sources/Helpers/IOKitBridge.m) / [lifecycle](https://github.com/chenjh16/CoffeeTea/blob/1c167ea573ed907885c1963e3082e5c4152bd8db/CoffeeTea/Sources/AppDelegate.swift)
- [S19] [XNU Sonoma: administrator-gated selector 9](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/RootDomainUserClient.cpp#L189-L207) / [selector 15](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/RootDomainUserClient.cpp#L494-L499)
- [S20] [Apple private assertion declarations](https://github.com/apple-oss-distributions/IOKitUser/blob/323ead896d04424f87184d8f6ff0cce811aab106/pwr_mgt.subproj/IOPMLibPrivate.h) / [entitlement enforcement](https://github.com/apple-oss-distributions/PowerManagement/blob/d415e45501842834a280930c3eed9186544a67f0/pmconfigd/PMAssertions.c)
- [S21] [XNU Sonoma: clear-mask reevaluates a closed lid](https://github.com/apple-oss-distributions/xnu/blob/2c2f96dc2b9a4408a43d3150ae9c105355ca3daa/iokit/Kernel/IOPMrootDomain.cpp#L4360-L4386)
