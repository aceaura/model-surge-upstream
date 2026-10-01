import 'package:flutter/material.dart';

import '../theme.dart';
import 'dialog_band.dart';

/// 确认弹窗的严重程度:色带/图标/确认钮颜色三者联动。
/// - [danger] 不可逆级联删除(删账号/模型/会话):红带+警告三角+红钮;
/// - [warning] 清空型破坏,对象本身保留(清消息/清日志):橙带+圆形叹号+橙钮。
enum ConfirmSeverity { danger, warning }

/// 统一确认弹窗(配合 `showDialog<bool>` 使用):通栏色带头( DialogBand ),
/// 白底正文,动作条为「描边取消 + 对应级别实色确认」——破坏性操作不再用
/// 品牌绿填充,与常规保存/创建按钮拉开语义距离。
///
/// 用法:
/// ```dart
/// final ok = await showDialog<bool>(context: context,
///   builder: (_) => const ConfirmDialog(...));
/// if (ok != true) return;
/// ```
class ConfirmDialog extends StatelessWidget {
  const ConfirmDialog({
    super.key,
    required this.title,
    required this.message,
    this.confirmLabel = '删除',
    this.severity = ConfirmSeverity.danger,
  });

  final String title;
  final String message;

  /// 危险确认钮文案(删除/清除/清空/确认删除)。
  final String confirmLabel;

  /// 严重程度,默认 [ConfirmSeverity.danger]。
  final ConfirmSeverity severity;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    final isDanger = severity == ConfirmSeverity.danger;
    return AlertDialog(
      // 头带要吃到弹窗圆角:显式裁剪,避免色带矩形戳出上角。
      clipBehavior: Clip.antiAlias,
      titlePadding: EdgeInsets.zero,
      contentPadding: const EdgeInsets.fromLTRB(22, 16, 22, 4),
      actionsPadding: const EdgeInsets.fromLTRB(22, 14, 22, 17),
      title: DialogBand(
        title: title,
        icon: isDanger ? Icons.warning_amber_rounded : Icons.error_outline,
        background: isDanger ? t.dangerSoft : t.warnSoft,
        foreground: isDanger ? t.dangerInk : t.warnInk,
      ),
      content: Text(message),
      actions: [
        OutlinedButton(
          onPressed: () => Navigator.of(context).pop(false),
          child: const Text('取消'),
        ),
        FilledButton(
          style: FilledButton.styleFrom(
            backgroundColor: isDanger ? t.danger : t.warn,
            foregroundColor: Colors.white,
          ),
          onPressed: () => Navigator.of(context).pop(true),
          child: Text(confirmLabel),
        ),
      ],
    );
  }
}
