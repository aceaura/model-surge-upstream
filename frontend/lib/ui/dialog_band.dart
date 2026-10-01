import 'package:flutter/material.dart';

/// 弹窗通栏色带头:图标 + 左对齐加粗标题,铺满弹窗顶部的色带。
/// 三级弹窗体系共用——危险(dangerSoft/dangerInk)、警示(warnSoft/warnInk)、
/// 常规(bg/dim)只换配色与图标,骨架完全一致,严重程度一眼可辨。
///
/// 配合 AlertDialog 使用:`titlePadding: EdgeInsets.zero` + `clipBehavior:
/// Clip.antiAlias`(色带才能吃进弹窗圆角)。
class DialogBand extends StatelessWidget {
  const DialogBand({
    super.key,
    required this.title,
    required this.icon,
    required this.background,
    required this.foreground,
  });

  final String title;
  final IconData icon;

  /// 色带底色(如 tokens.dangerSoft / warnSoft / bg)。
  final Color background;

  /// 图标与标题色(如 tokens.dangerInk / warnInk / dim)。
  final Color foreground;

  @override
  Widget build(BuildContext context) {
    return Container(
      color: background,
      padding: const EdgeInsets.symmetric(horizontal: 22, vertical: 15),
      child: Row(
        children: [
          Icon(icon, size: 19, color: foreground),
          const SizedBox(width: 9),
          Expanded(
            child: Text(
              title,
              style: TextStyle(
                fontSize: 16,
                fontWeight: FontWeight.w700,
                color: foreground,
              ),
            ),
          ),
        ],
      ),
    );
  }
}
