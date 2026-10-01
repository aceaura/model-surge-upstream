import 'package:flutter/material.dart';

import '../theme.dart';
import 'dialog_header.dart';

/// 统一确认弹窗(配合 `showDialog<bool>` 使用):头部仍是 DialogHeader 的
/// 居中标题+通栏分隔线,动作条为 CC Switch 式「描边取消 + 红底危险确认」,
/// 破坏性操作不再用品牌绿填充,与常规保存/创建按钮拉开语义距离。
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
  });

  final String title;
  final String message;

  /// 危险确认钮文案(删除/清除/清空/确认删除)。
  final String confirmLabel;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return AlertDialog(
      titlePadding: EdgeInsets.zero,
      title: DialogHeader(title: title),
      content: Text(message),
      actions: [
        OutlinedButton(
          onPressed: () => Navigator.of(context).pop(false),
          child: const Text('取消'),
        ),
        FilledButton(
          style: FilledButton.styleFrom(
            backgroundColor: t.danger,
            foregroundColor: Colors.white,
          ),
          onPressed: () => Navigator.of(context).pop(true),
          child: Text(confirmLabel),
        ),
      ],
    );
  }
}
