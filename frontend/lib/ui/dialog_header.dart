import 'package:flutter/material.dart';

import '../theme.dart';

/// 弹窗统一头部:标题水平居中,下方一条通栏细分隔线,把头部与内容分开。
/// 配合 AlertDialog 的 titlePadding: EdgeInsets.zero 使用,分隔线才能通栏。
class DialogHeader extends StatelessWidget {
  const DialogHeader({super.key, required this.title});

  final String title;

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(24, 20, 24, 14),
          child: Text(
            title,
            textAlign: TextAlign.center,
            style: TextStyle(
              fontSize: 17,
              fontWeight: FontWeight.w700,
              color: t.ink,
            ),
          ),
        ),
        Divider(height: 1, thickness: 1, color: t.border),
      ],
    );
  }
}
