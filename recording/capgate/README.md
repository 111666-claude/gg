# capgate — 渲染后端能力一致性质量门禁

同一份 `recording.Recording` 回放至不同后端时，最隐蔽的问题是“声明支持、
没有报错、却少画了一个命令”。`capgate` 用一张确定性的**命令矩阵**，对每个
“后端 × 用例”交叉核对四件事：

1. **Capabilities 声明**（`recording.Capabilities` / `recording.CapableBackend`）；
2. **预检结果**（`recording.Preflight`：缺失能力、NaN/Inf、资源引用、栈深）；
3. **实际调用**（`CallLog` 记录到的 `Backend` 方法与参数细节）；
4. **回退路径**（显式 `Fallbacks` 或载体能力，例如 `ClipRoundRect → SetClip`、
   `StrokeText → DrawText` lowering）。

门禁是纯 Go 的：参考替身（Spy）与 CPU `recording/backends/raster` 后端都不
初始化任何 GPU。

## 命令矩阵（44 例）

`BuildMatrix` 覆盖：

- 路径填充 / 描边、矩形填充 / 描边（`path/*`、`rect/*`）；
- 圆角矩形曲线保真（`roundrect/*`，校验曲线动词确实到达后端）；
- 路径裁剪、圆角裁剪 lowering、清除裁剪（`clip/*`）；
- 图片（`image/draw`，校验非空图像到达）；
- 文字填充与 `StrokeText → DrawText` lowering（`text/*`）；
- linear / radial / sweep 三种渐变（`gradient/*`，校验画笔类型）；
- `recording.AllBlendModes()` 中的**每一个**混合模式（29 例，校验精确 mode 值）；
- Save/Restore 状态栈（`state/save-restore`）。

## 可定位的报告类别

每个偏差都是一条带 `subject`、`case_id`、`command_index`、`phase` 与说明的
`Finding`，JSONL 中可直接定位：

| code | 含义 |
|------|------|
| `declared_supported_error` | 声明支持，但 `Begin`/命令/`End` 返回错误 |
| `undeclared_draw` | 声明不支持且无回退，却发生了绘制调用 |
| `capability_list_unstable` | 连续探测 `Capabilities()` 得到不同画像/缺失清单 |
| `backend_midrun_failure` | 后端在第 N 条命令中途失败（`PlaybackError` 带索引/类型） |
| `silent_drop` | 声明支持、无错误，但期望调用从未发生（**静默丢绘制**） |
| `missing_optional_interface` | 声明 `blend_modes` 却未实现 `recording.BlendModeBackend` |
| `recording_mutated` | 回放改写了输入 Recording / 资源池（SHA-256 指纹变化） |
| `stack_unbalanced` | Save/Restore 不配对或 Restore 下溢 |

## 测试替身的不可改写保证

`Spy` 实现 `Backend`、`BlendModeBackend`、`CapableBackend`、`ErrorReporter`，
记录有序 `CallRecord`，跟踪状态栈深度/下溢，并可注入故障。

`Fingerprint(rec)` 对命令（含 dash 切片等）、每个资源池路径的
verbs/coords、画笔、图像像素与字体计算 SHA-256。门禁在回放前后各取一次指纹
比对，因此后端一旦改写池化路径（见 `MutatePaths` 故障）立即被抓住。

边界用例见 `edge_test.go`：空命令、1000 层深嵌套（含 `MaxStateDepth` 预检）、
8 类 NaN/Inf、预取消与播放中途取消、同一 Recording 重复 5 次回放指纹恒定。

## 可重复的 JSONL 与故障注入样本

```bash
# 生成 artifacts/capgate/report.jsonl 与 fault-injection.jsonl
go run ./cmd/capgate -out artifacts/capgate

# 仅跑门禁测试（含 golden 校验、race 可选）
go test ./recording/capgate/...
go test -race ./recording/capgate/...
```

- 输出**不含时间戳**，map 键由 `encoding/json` 排序，连续两次运行字节一致
  （测试 `TestFaultJSONLReproducible` 与 CI 中的 sha256 比对保证）。
- 9 个故障注入样本（`FaultSamples`）逐一证明门禁抓得住对应缺陷，golden 见
  [`testdata/fault-injection.jsonl`](testdata/fault-injection.jsonl)。
- 更新 golden：`CAPGATE_UPDATE_GOLDEN=1 go test ./recording/capgate/ -run TestCheckedInFaultSampleIsCurrent`。

当前参考结果：两个主体（perfect-spy、raster-cpu）× 44 例 = 88 条结果，
`87 pass + 1 pass_via_fallback`，0 个 error finding；9/9 注入故障全部被抓。

## 接入真实后端

```go
subj := capgate.Subject{
    Name: "raster-cpu",
    New:  func() recording.Backend {
        return capgate.NewInstrumented("raster-cpu", raster.NewBackend())
    },
}
rep := capgate.Run([]capgate.Subject{subj}, capgate.BuildMatrix(capgate.MatrixOptions{}), face)
_ = rep.WriteJSONL(os.Stdout)
if rep.Summary.ErrorFindings != 0 {
    os.Exit(1)
}
```

后端可用 `recording.Capabilities` + `recording.CapableBackend` 自报能力，用
`recording.Preflight` 在回放前得到稳定排序的缺失清单；播放用
`(*Recording).PlaybackContext(ctx, b)` 获得取消与带命令位置的 `PlaybackError`。
