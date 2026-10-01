import 'package:flutter/material.dart';

import '../theme.dart';

/// 统一确认弹窗(配合 `showDialog<bool>` 使用):浅红通栏头带
/// (dangerSoft 底 + 警告三角 + dangerInk 深红左对齐标题,无分隔线),
/// 白底正文,动作条为「描边取消 + 红底危险确认」——破坏性操作不再用
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
  });

  final String title;
  final String message;

  /// 危险确认钮文案(删除/清除/清空/确认删除)。
  final String confirmLabel;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return AlertDialog(
      // 头带要吃到弹窗圆角:显式裁剪,避免 dangerSoft 矩形戳出上角。
      clipBehavior: Clip.antiAlias,
      titlePadding: EdgeInsets.zero,
      contentPadding: const EdgeInsets.fromLTRB(22, 16, 22, 4),
      actionsPadding: const EdgeInsets.fromLTRB(22, 14, 22, 17),
      title: Container(
        color: t.dangerSoft,
        padding: const EdgeInsets.symmetric(horizontal: 22, vertical: 15),
        child: Row(
          children: [
            Icon(Icons.warning_amber_rounded, size: 19, color: t.dangerInk),
            const SizedBox(width: 9),
            Expanded(
              child: Text(
                title,
                style: TextStyle(
                  fontSize: 16,
                  fontWeight: FontWeight.w700,
                  color: t.dangerInk,
                ),
              ),
            ),
          ],
        ),
      ),
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
