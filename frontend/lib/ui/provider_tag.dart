import 'package:flutter/material.dart';

/// 供应商 id 标记:CC Switch 式浅蓝底小圆角标签(无边框),
/// 替代 Material Chip 的灰框样式。
class ProviderTag extends StatelessWidget {
  const ProviderTag(this.id, {super.key});

  final String id;

  @override
  Widget build(BuildContext context) {
    final dark = Theme.of(context).brightness == Brightness.dark;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 2),
      decoration: BoxDecoration(
        color: dark ? const Color(0xFF2B4560) : const Color(0xFFE7F0FB),
        borderRadius: BorderRadius.circular(7),
      ),
      child: Text(
        id,
        style: TextStyle(
          fontSize: 11.5,
          fontWeight: FontWeight.w500,
          color: dark ? const Color(0xFF9CC6EC) : const Color(0xFF3D7BC4),
        ),
      ),
    );
  }
}
