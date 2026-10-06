import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';
import '../ui/confirm_dialog.dart';
import '../ui/feedback.dart';
import '../ui/hover_card.dart';
import '../ui/page_header.dart';
import '../ui/provider_avatar.dart';
import '../ui/provider_tag.dart';
import '../ui/summary_band.dart';
import '../ui/top_toast.dart';
import 'model_form.dart';

/// 搜索词种子:主壳在账号页点「模型」时下发,每次点击都是新实例,
/// 保证同一账号连点两次也能触发监听。模型页收到后写入搜索框并按之过滤。
class SearchSeed {
  const SearchSeed(this.text);

  final String text;
}

/// 全部账号下的模型总览。侧栏一级导航页,不按账号分组,
/// 卡片形态与账号内模型页一致,副标题额外标注所属账号以作区分。
class AllModelsPage extends StatefulWidget {
  const AllModelsPage({
    super.key,
    required this.client,
    required this.onOpenSettings,
    this.searchSeed,
    this.onOpenUsage,
    this.active = false,
  });

  final ApiClient client;
  final VoidCallback onOpenSettings;

  /// 外部下发的搜索词种子(账号页「模型」按钮跳转时携带账号名)。
  final ValueNotifier<SearchSeed?>? searchSeed;

  /// 点「统计」时通知主壳跳到用量页,并以该模型标识作为过滤器。
  final void Function(UpstreamModel model)? onOpenUsage;

  /// 是否正处于前台(由主壳按当前导航传入)。页在 IndexedStack 里常驻,
  /// 别处的改动(删账号级联删模型等)不会触发本页重建,重新激活时刷新。
  final bool active;

  @override
  State<AllModelsPage> createState() => _AllModelsPageState();
}

class _AllModelsPageState extends State<AllModelsPage> {
  late Future<(List<UpstreamModel>, List<Account>, List<ProviderSpec>)>
      _future = _load();
  final _toggling = <String>{};
  final _testing = <String>{};
  final _searchController = TextEditingController();
  String _query = '';

  /// 拖拽排序的用户序(全量模型 id)。非空时乐观覆盖服务器返回顺序,
  /// 后台落库失败即清掉回落服务器序;按 id 合并,服务器侧新增模型随其
  /// 原序追加在尾。
  List<String>? _order;

  /// 最近一次成功加载的数据。重拉(_reload)期间继续渲染它,否则
  /// FutureBuilder 回到等待态,整页列表被 loading 替换闪一下。
  (List<UpstreamModel>, List<Account>, List<ProviderSpec>)? _lastData;

  /// 落库成功后待释放的 _order 快照:重拉带回新数据(服务器序已是手动序)
  /// 时才清 _order,顺序不跳;期间被更新的拖拽改写则不动(见 _persistOrder)。
  List<String>? _releaseOrderOnData;

  /// 非空时页内内联显示整页表单(替代列表),侧边栏保持可见。
  ModelForm? _form;

  @override
  void initState() {
    super.initState();
    widget.searchSeed?.addListener(_onSearchSeed);
  }

  @override
  void didUpdateWidget(AllModelsPage oldWidget) {
    super.didUpdateWidget(oldWidget);
    // 重新激活(false→true)时刷新:别处的改动(删账号级联删模型等)
    // 不会触发常驻页重建,不刷新会在列表里看到已删模型。
    if (!oldWidget.active && widget.active) _reload();
  }

  @override
  void dispose() {
    widget.searchSeed?.removeListener(_onSearchSeed);
    _searchController.dispose();
    super.dispose();
  }

  /// 种子到达:搜索框填入账号名并立即按之过滤。
  /// 程序化改 controller 不触发 onChanged,这里直接同步 _query。
  void _onSearchSeed() {
    final seed = widget.searchSeed?.value;
    if (seed == null) return;
    _searchController.text = seed.text;
    setState(() => _query = seed.text);
  }

  Future<(List<UpstreamModel>, List<Account>, List<ProviderSpec>)>
      _load() async {
    final models = await widget.client.listModels();
    final accounts = await widget.client.listAccounts();
    final providers = await widget.client.listProviders();
    return (models, accounts, providers);
  }

  // 块体而非 => 箭头:setState 断言回调不得返回 Future,箭头写法会把
  // 赋值表达式的 Future 带回去,debug 下每次 _reload 都触发断言。
  void _reload() => setState(() {
        _future = _load();
      });

  /// 手动序与服务器列表的合并:_order 里仍在的模型按拖拽序排前,
  /// 其余(新增等)按服务器序追加。_order 为空即原样。
  List<UpstreamModel> _ordered(List<UpstreamModel> models) {
    final order = _order;
    if (order == null) return models;
    final remaining = <String, UpstreamModel>{for (final m in models) m.id: m};
    final out = <UpstreamModel>[];
    for (final id in order) {
      final m = remaining.remove(id);
      if (m != null) out.add(m);
    }
    for (final m in models) {
      if (remaining.containsKey(m.id)) out.add(m);
    }
    return out;
  }

  /// 拖拽落点:先乐观换序渲染,再后台落库;失败提示并回落服务器序。
  /// onReorderItem 的 newIndex 已是移除旧项后的修正值,直接插入。
  void _onReorder(List<UpstreamModel> shown, int oldIndex, int newIndex) {
    final ids = shown.map((m) => m.id).toList();
    final moved = ids.removeAt(oldIndex);
    ids.insert(newIndex, moved);
    setState(() => _order = ids);
    _persistOrder(ids);
  }

  Future<void> _persistOrder(List<String> ids) async {
    try {
      await widget.client.reorderModels(ids);
      if (!mounted) return;
      // 落库成功后服务器序已等于手动序,挂起释放标记后重拉:新数据到达
      // 才清 _order(见 build),既不闪 loading 也不跳序;不留着 _order,
      // 否则它会永久盖住外部(API/他端)后来的顺序变更——激活重拉也救不回。
      // 连拖时 _order 已被下一次拖拽改写,只对得上的那次才挂。
      if (_order != null && _sameIds(_order!, ids)) {
        _releaseOrderOnData = ids;
      }
      _reload();
    } catch (e) {
      if (!mounted) return;
      showError(context, e);
      setState(() => _order = null);
    }
  }

  bool _sameIds(List<String> a, List<String> b) {
    if (a.length != b.length) return false;
    for (var i = 0; i < a.length; i++) {
      if (a[i] != b[i]) return false;
    }
    return true;
  }

  /// 卡片尾部的小号操作按钮:18 图标 + 弱色(与账号页一致)。
  Widget _action(
      IconData icon, String tooltip, VoidCallback onPressed, AppTokens t) {
    return IconButton(
      tooltip: tooltip,
      icon: Icon(icon, size: 18, color: t.faint),
      splashRadius: 18,
      visualDensity: VisualDensity.compact,
      onPressed: onPressed,
    );
  }

  /// 整页表单(CC Switch 式):页内内联替换列表,不再推根路由遮盖侧边栏。
  void _openForm(ModelForm form) => setState(() => _form = form);

  /// 表单收尾:关闭内联表单,已保存则重载列表。
  void _closeForm(bool saved) {
    setState(() => _form = null);
    if (saved) _reload();
  }

  /// 面包屑账号级点击:关掉表单,搜索框落到该账号名,列表即按其过滤
  /// (等同账号页「模型」按钮下发的搜索种子效果)。
  void _showAccountModels(String account) {
    setState(() {
      _form = null;
      _searchController.text = account;
      _query = account;
    });
  }

  void _create(List<Account> accounts, List<ProviderSpec> providers,
      String initialAccount) {
    _openForm(ModelForm(
      client: widget.client,
      accounts: accounts,
      providers: providers,
      initialAccount: initialAccount,
      onDone: _closeForm,
    ));
  }

  void _edit(UpstreamModel model, List<Account> accounts,
      List<ProviderSpec> providers) {
    _openForm(ModelForm(
      client: widget.client,
      accounts: accounts,
      providers: providers,
      initialAccount: model.account,
      onDone: _closeForm,
      onShowAccount: _showAccountModels,
      editing: model,
    ));
  }

  /// 拷贝(CC Switch 式):以该模型配置预填新建整页表单,标识加 -copy 后缀需自行调整。
  void _copy(UpstreamModel model, List<Account> accounts,
      List<ProviderSpec> providers) {
    _openForm(ModelForm(
      client: widget.client,
      accounts: accounts,
      providers: providers,
      initialAccount: model.account,
      onDone: _closeForm,
      onShowAccount: _showAccountModels,
      copyFrom: model,
    ));
  }

  Future<void> _toggle(UpstreamModel model) async {
    setState(() => _toggling.add(model.id));
    try {
      await widget.client.updateModel(id: model.id, enabled: !model.enabled);
      _reload();
    } catch (e) {
      if (mounted) showError(context, e);
    } finally {
      if (mounted) setState(() => _toggling.remove(model.id));
    }
  }

  /// 检测连通性(参考 CC Switch):向真实上游发最小探测请求,
  /// 链路/凭据/模型名任一不通都会在结果里说明。结果用顶部提示框呈现,
  /// 行内容不因此抖动。
  Future<void> _test(UpstreamModel model) async {
    setState(() => _testing.add(model.id));
    try {
      final res = await widget.client.testModel(model.id);
      if (!mounted) return;
      if (res.ok) {
        TopToast.show(context,
            '${model.id} 连通正常 · HTTP ${res.statusCode} · ${res.latencyMs} ms');
      } else {
        final detail = res.statusCode > 0
            ? 'HTTP ${res.statusCode} · ${res.latencyMs} ms\n${res.error}'
            : res.error;
        TopToast.show(context, '${model.id} 检测失败：$detail', error: true);
      }
    } catch (e) {
      if (mounted) showError(context, e);
    } finally {
      if (mounted) setState(() => _testing.remove(model.id));
    }
  }

  Future<void> _delete(UpstreamModel model) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => ConfirmDialog(
        title: '删除模型 ${model.id}？',
        message: '删除模型不影响其所属账号。',
        confirmLabel: '确认删除',
      ),
    );
    if (confirmed != true) return;
    try {
      await widget.client.deleteModel(model.id);
      _reload();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  /// 头像字母取 id 中「账号/」之后的模型名,颜色随名哈希。
  String _shortName(String id) {
    final i = id.indexOf('/');
    return i >= 0 && i + 1 < id.length ? id.substring(i + 1) : id;
  }

  @override
  Widget build(BuildContext context) {
    // 表单打开时整幅替换列表内容(侧边栏在主壳,不受影响)。
    final form = _form;
    if (form != null) return form;
    final t = context.tokens;
    return FutureBuilder<(List<UpstreamModel>, List<Account>, List<ProviderSpec>)>(
      future: _future,
      builder: (context, snapshot) {
        if (snapshot.connectionState == ConnectionState.done &&
            !snapshot.hasError &&
            snapshot.data != null) {
          _lastData = snapshot.data;
          // 落库后的重拉带回服务器序(=已落库的手动序),此刻放手 _order:
          // 渲染顺序不变,不跳。_order 期间被更新的拖拽改写则保留。
          final release = _releaseOrderOnData;
          if (release != null) {
            _releaseOrderOnData = null;
            if (_order != null && _sameIds(_order!, release)) _order = null;
          }
        }
        final data = _lastData;
        final loaded = data != null;
        final models =
            data == null ? const <UpstreamModel>[] : _ordered(data.$1);
        final accounts = data == null ? const <Account>[] : data.$2;
        final providers = data == null ? const <ProviderSpec>[] : data.$3;

        // 摘要带:按协议计数 + 搜索过滤(标识/上游模型名/所属账号)。
        final counts = <String, int>{};
        for (final m in models) {
          counts[m.protocol] = (counts[m.protocol] ?? 0) + 1;
        }
        final q = _query.toLowerCase();
        final visible = q.isEmpty
            ? models
            : models
                .where((m) =>
                    m.id.toLowerCase().contains(q) ||
                    m.nativeModel.toLowerCase().contains(q) ||
                    m.account.toLowerCase().contains(q))
                .toList();

        final Widget content;
        if (!loaded) {
          content = snapshot.hasError
              ? ErrorPanel(
                  error: snapshot.error!,
                  onRetry: _reload,
                  onOpenSettings: widget.onOpenSettings,
                )
              : const Center(child: CircularProgressIndicator());
        } else if (visible.isEmpty) {
          content = Center(
            child: Text(
              models.isEmpty ? '还没有配置任何模型。' : '没有匹配的模型。',
              style: TextStyle(color: t.faint),
            ),
          );
        } else if (_query.isEmpty) {
          content = ReorderableListView.builder(
            padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
            // 拖拽柄只在行首;默认柄会让整行抢占滚动手势
            buildDefaultDragHandles: false,
            // 默认 proxyDecorator 垫一层白色 Material,拖起时卡片底部
            // padding 处露白边;透明 Material 保持悬浮层与列表底色一致。
            proxyDecorator: (child, index, animation) =>
                Material(type: MaterialType.transparency, child: child),
            itemCount: visible.length,
            onReorderItem: (o, n) => _onReorder(visible, o, n),
            itemBuilder: (context, i) => Padding(
              key: ValueKey(visible[i].id),
              padding:
                  EdgeInsets.only(bottom: i == visible.length - 1 ? 0 : 12),
              child:
                  _modelCard(visible[i], accounts, providers, t, dragIndex: i),
            ),
          );
        } else {
          // 过滤态禁拖:子集换序映射回全量顺序有歧义
          content = ListView.separated(
            padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
            itemCount: visible.length,
            separatorBuilder: (_, _) => const SizedBox(height: 12),
            itemBuilder: (context, i) =>
                _modelCard(visible[i], accounts, providers, t),
          );
        }

        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            PageHeader(
              title: '模型',
              // 页头不放刷新:数据随操作自动重载,重试入口在错误面板
              trailing: [
                FilledButton.icon(
                  onPressed: loaded && accounts.isNotEmpty
                      ? () => _create(accounts, providers, accounts.first.name)
                      : null,
                  icon: const Icon(Icons.add, size: 16),
                  label: const Text('新建模型'),
                ),
              ],
            ),
            if (loaded)
              SummaryBand(
                summary: '已配置 ${models.length} 个模型',
                stats: [
                  for (final e in counts.entries)
                    BandStat(label: e.key, count: e.value, colorKey: e.key),
                ],
                searchHint: '搜索模型标识、上游模型名或账号',
                controller: _searchController,
                onSearch: (v) => setState(() => _query = v),
              ),
            Expanded(child: content),
          ],
        );
      },
    );
  }

  /// 模型行卡片。dragIndex 非空时行首附拖拽柄(仅未过滤列表;过滤子集
  /// 换序映射回全量顺序有歧义,过滤态禁拖)。
  Widget _modelCard(UpstreamModel m, List<Account> accounts,
      List<ProviderSpec> providers, AppTokens t,
      {int? dragIndex}) {
    // 头像继承账号所属提供商的官方 Logo;账号找不到时回落模型名首字母。
    final providerId = accounts
            .where((a) => a.name == m.account)
            .firstOrNull
            ?.providerId ??
        _shortName(m.id);
    return HoverCard(
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 14),
        child: IntrinsicHeight(
          child: Row(
            children: [
              // 拖拽柄常驻行首(仿 CC Switch 的 GripVertical),弱色不抢眼;
              // 命中区撑满行高、28 宽——18 图标本身太小,整条左边带都可抓。
              // 透明底色不能省:无色的 Container 不参与命中测试。
              if (dragIndex != null)
                ReorderableDragStartListener(
                  index: dragIndex,
                  child: MouseRegion(
                    cursor: SystemMouseCursors.grab,
                    child: Container(
                      width: 28,
                      height: double.infinity,
                      color: Colors.transparent,
                      alignment: Alignment.centerLeft,
                      child: Icon(Icons.drag_indicator, size: 18, color: t.faint),
                    ),
                  ),
                ),
              ProviderAvatar(providerId: providerId),
              const SizedBox(width: 13),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      children: [
                        Flexible(
                          child: Text(
                            m.id,
                            overflow: TextOverflow.ellipsis,
                            style: TextStyle(
                              fontSize: 15,
                              fontWeight: FontWeight.w600,
                              color: t.ink,
                            ),
                          ),
                        ),
                        const SizedBox(width: 8),
                        ProviderTag(m.protocol),
                      ],
                    ),
                    const SizedBox(height: 4),
                    Text(
                      [
                        '账号 ${m.account}',
                        '上游 ${m.nativeModel}',
                        '窗口 ${m.contextWindow == 0 ? '未声明' : '${tokensToK(m.contextWindow)}k'}',
                        if (m.defaults.isNotEmpty) '默认参数 ${m.defaults.length} 项',
                        if (m.overrides.isNotEmpty) '覆盖参数 ${m.overrides.length} 项',
                      ].join('   '),
                      overflow: TextOverflow.ellipsis,
                      style: TextStyle(fontSize: 12, color: t.faint),
                    ),
                  ],
                ),
              ),
              const SizedBox(width: 8),
              if (_toggling.contains(m.id))
                const SizedBox(
                  width: 24,
                  height: 24,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              else
                Switch(
                  value: m.enabled,
                  onChanged: (_) => _toggle(m),
                ),
              _action(Icons.edit_outlined, '编辑',
                  () => _edit(m, accounts, providers), t),
              _action(Icons.copy_outlined, '拷贝',
                  () => _copy(m, accounts, providers), t),
              // 排位仿 CC Switch(编辑/拷贝/检测/统计/删除),检测中换行内 spinner。
              if (_testing.contains(m.id))
                const Padding(
                  padding: EdgeInsets.all(12),
                  child: SizedBox(
                    width: 18,
                    height: 18,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  ),
                )
              else
                _action(Icons.network_check, '检测连通性', () => _test(m), t),
              if (widget.onOpenUsage != null)
                _action(Icons.bar_chart, '统计', () => widget.onOpenUsage!(m), t),
              _action(Icons.delete_outline, '删除', () => _delete(m), t),
            ],
          ),
        ),
      ),
    );
  }
}
