import 'package:flutter/material.dart';

import '../api_client.dart';
import '../settings_store.dart';
import '../theme.dart';
import '../ui/page_header.dart';
import 'proxy_page.dart';
import 'settings_page.dart';

/// 设置枢纽页:代理服务与连接两个子页以页内分页签合并,
/// 侧栏只保留一个「设置」入口。TabController 持有在 State 里,
/// 外壳重建(如切换导航)不会把分页签弹回第一页。
class SettingsHubPage extends StatefulWidget {
  const SettingsHubPage({
    super.key,
    required this.client,
    required this.initial,
    required this.onSaved,
  });

  final ApiClient client;
  final Settings initial;
  final Future<void> Function(Settings) onSaved;

  @override
  State<SettingsHubPage> createState() => _SettingsHubPageState();
}

class _SettingsHubPageState extends State<SettingsHubPage>
    with SingleTickerProviderStateMixin {
  late final TabController _tabs = TabController(length: 2, vsync: this);

  @override
  void dispose() {
    _tabs.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        const PageHeader(title: '设置', trailing: []),
        Padding(
          padding: const EdgeInsets.fromLTRB(24, 0, 24, 4),
          child: Align(
            alignment: Alignment.centerLeft,
            child: TabBar(
              controller: _tabs,
              isScrollable: true,
              tabAlignment: TabAlignment.start,
              labelColor: t.primaryInk,
              unselectedLabelColor: t.faint,
              indicatorColor: t.primary,
              dividerColor: t.border,
              tabs: const [Tab(text: '代理服务'), Tab(text: '连接')],
            ),
          ),
        ),
        // 两页互为邻页,TabBarView 会一并保活:切换分页签不重载代理配置。
        Expanded(
          child: TabBarView(
            controller: _tabs,
            children: [
              ProxyPage(
                client: widget.client,
                onOpenSettings: () => _tabs.animateTo(1),
              ),
              SettingsPage(
                initial: widget.initial,
                onSaved: widget.onSaved,
                embedded: true,
              ),
            ],
          ),
        ),
      ],
    );
  }
}
