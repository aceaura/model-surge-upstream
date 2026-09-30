import 'package:flutter/material.dart';

import '../api_client.dart';
import '../settings_store.dart';
import '../ui/collapsible_section.dart';
import '../ui/page_header.dart';
import 'proxy_page.dart';
import 'settings_page.dart';

/// 设置枢纽页:代理服务与连接两个分栏以 CC Switch 式可折叠卡片呈现,
/// 侧栏只保留一个「设置」入口。点击分栏行展开内容,内容常驻树中,
/// 折叠不丢已填状态。
class SettingsHubPage extends StatelessWidget {
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
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        const PageHeader(title: '设置', trailing: []),
        Expanded(
          child: SingleChildScrollView(
            padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                CollapsibleSection(
                  icon: Icons.lan_outlined,
                  title: '代理服务',
                  subtitle: '独立端口与密钥,把命名模型按三协议原生形态转发',
                  child: ProxyPage(client: client),
                ),
                const SizedBox(height: 14),
                CollapsibleSection(
                  icon: Icons.link_outlined,
                  title: '连接',
                  subtitle: initial.baseUrl.isEmpty
                      ? '尚未配置服务地址'
                      : initial.baseUrl,
                  child: SettingsPage(
                    initial: initial,
                    onSaved: onSaved,
                    embedded: true,
                  ),
                ),
              ],
            ),
          ),
        ),
      ],
    );
  }
}
