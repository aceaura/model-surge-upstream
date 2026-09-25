import 'package:flutter/material.dart';

/// 供应商头像:圆角方块 + id 首字母,配色按 id 哈希从柔和色板取,
/// 同一 provider 在任何页面颜色一致(CC Switch 式供应商图标位)。
class ProviderAvatar extends StatelessWidget {
  const ProviderAvatar({super.key, required this.providerId, this.size = 40});

  final String providerId;
  final double size;

  /// (底色, 字色) 柔和色板,明暗两套。
  static const _light = [
    (Color(0xFFDDF2E4), Color(0xFF15803D)), // 绿
    (Color(0xFFE0ECFD), Color(0xFF2E49D6)), // 蓝
    (Color(0xFFF0E9FD), Color(0xFF6D4BD8)), // 紫
    (Color(0xFFFCF0DC), Color(0xFFB37209)), // 橙
    (Color(0xFFFBE7E7), Color(0xFFC03535)), // 红
    (Color(0xFFDFF2F0), Color(0xFF0F766E)), // 青
  ];
  static const _dark = [
    (Color(0xFF173627), Color(0xFF9BE7C2)),
    (Color(0xFF1B2A4A), Color(0xFF9DB4F5)),
    (Color(0xFF2A2145), Color(0xFFC4B0F5)),
    (Color(0xFF3A2E17), Color(0xFFE8C078)),
    (Color(0xFF3D2020), Color(0xFFE8A0A0)),
    (Color(0xFF14322F), Color(0xFF7ED6CC)),
  ];

  @override
  Widget build(BuildContext context) {
    final dark = Theme.of(context).brightness == Brightness.dark;
    final palette = dark ? _dark : _light;
    var hash = 0;
    for (final unit in providerId.codeUnits) {
      hash = (hash * 33 + unit) & 0x7fffffff;
    }
    // 终搅一遍再取槽位,避免 anthropic/openai 这类近邻 id 撞同色。
    hash = (hash * 2654435761) & 0x7fffffff;
    final (bg, fg) = palette[hash % palette.length];
    final letter = providerId.isEmpty ? '?' : providerId[0].toUpperCase();
    return Container(
      width: size,
      height: size,
      decoration: BoxDecoration(
        color: bg,
        borderRadius: BorderRadius.circular(size * 0.28),
      ),
      alignment: Alignment.center,
      child: Text(
        letter,
        style: TextStyle(
          fontSize: size * 0.42,
          fontWeight: FontWeight.w700,
          color: fg,
        ),
      ),
    );
  }
}
