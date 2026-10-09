import 'package:flutter/material.dart';

import '../autostart.dart';
import '../theme.dart';
import '../ui/collapsible_section.dart';

/// 设置页「启动」分栏:开机自启动开关,状态直读注册表 Run 键,
/// 切换即写即生效,不走表单保存。
class AutostartSection extends StatefulWidget {
  const AutostartSection({super.key, this.probe, this.apply});

  /// 可注入的状态读取/写入,测试替身用;缺省走注册表实现。
  final Future<bool> Function()? probe;
  final Future<void> Function(bool)? apply;

  @override
  State<AutostartSection> createState() => _AutostartSectionState();
}

class _AutostartSectionState extends State<AutostartSection> {
  bool? _enabled; // null=读取中

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final v = await (widget.probe ?? Autostart.isEnabled)();
    if (mounted) setState(() => _enabled = v);
  }

  Future<void> _set(bool v) async {
    await (widget.apply ?? Autostart.setEnabled)(v);
    if (mounted) setState(() => _enabled = v);
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return CollapsibleSection(
      icon: Icons.rocket_launch_outlined,
      title: '启动',
      subtitle: switch (_enabled) {
        null => '读取中…',
        true => '已开启:登录 Windows 后自动启动',
        false => '已关闭:登录后不自动启动',
      },
      child: Row(
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text('开机自启动',
                    style: TextStyle(
                        fontSize: 13, fontWeight: FontWeight.w600, color: t.ink)),
                const SizedBox(height: 2),
                Text('登录 Windows 后自动打开配置中心;重复启动只会激活已有窗口',
                    style: TextStyle(fontSize: 12, color: t.faint)),
              ],
            ),
          ),
          Switch(
            key: const ValueKey('autostart-enabled'),
            value: _enabled ?? false,
            onChanged: _enabled == null ? null : _set,
          ),
        ],
      ),
    );
  }
}
