import 'dart:async';

import 'package:flutter/material.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';
import '../ui/confirm_dialog.dart';
import '../ui/feedback.dart';
import '../ui/page_header.dart';
import '../ui/styled_dropdown.dart';

/// 进程日志页：轮询拉取服务端环形缓冲里的进程日志（启动事件、HTTP 请求、
/// 模型解析、转发面应用、对话补全等），级别过滤 + 自动跟随尾部。
/// 只读展示，清空作用于服务端缓冲。
class LogsPage extends StatefulWidget {
  const LogsPage({
    super.key,
    required this.client,
    required this.active,
    this.onOpenSettings,
  });

  final ApiClient client;

  /// 当前是否停留在本页：离开页签时停轮询，避免后台空转。
  final bool active;
  final VoidCallback? onOpenSettings;

  @override
  State<LogsPage> createState() => _LogsPageState();
}

class _LogsPageState extends State<LogsPage> {
  static const _pollInterval = Duration(milliseconds: 1500);
  static const _levelOptions = ['all', 'info', 'warn', 'error'];
  static const _levelLabels = {
    'all': '全部级别',
    'info': 'info',
    'warn': 'warn',
    'error': 'error',
  };

  /// 转发/解析/对话条目的 msg 里带 model=/account= 键值,筛选按它匹配;
  /// 没有该键的条目(纯 HTTP 管理面、启动事件)在对应筛选生效时不显示。
  static final _modelRe = RegExp(r'(?:^|\s)model=(\S+)');
  static final _accountRe = RegExp(r'(?:^|\s)account=(\S+)');

  // 列宽：时间 / 级别 / 来源 三列定宽，内容列吃满剩余宽度。
  static const _colTime = 64.0;
  static const _colLevel = 52.0;
  static const _colSource = 68.0;
  static const _colGap = 12.0;

  final List<LogEntry> _entries = [];
  final ScrollController _scroll = ScrollController();
  Timer? _timer;
  int _since = 0;
  String _level = 'all';
  String _modelFilter = 'all';
  String _accountFilter = 'all';

  /// 下拉选项的基底:当前配置的模型/账号。日志里出现过的其他值(如已删除
  /// 的模型)并入选项,保证历史条目也可筛。
  List<String> _knownModels = [];
  List<String> _knownAccounts = [];
  bool _follow = true;

  /// 跟随是否由「用户往上翻」自动暂停的：滚回底部时自动恢复；
  /// 手动按开关关闭的不自动恢复，尊重显式意图。
  bool _autoPaused = false;
  bool _loading = true;
  Object? _error;

  @override
  void initState() {
    super.initState();
    _scroll.addListener(_onUserScroll);
    _loadFilterChoices();
    _reload();
  }

  Future<void> _loadFilterChoices() async {
    try {
      final models = await widget.client.listModels();
      final accounts = await widget.client.listAccounts();
      if (!mounted) return;
      setState(() {
        _knownModels = [for (final m in models) m.id];
        _knownAccounts = [for (final a in accounts) a.name];
      });
    } catch (_) {
      // 配置列表拉取失败不挡日志展示:选项退化为只从日志条目解析。
    }
  }

  static String? _modelOf(LogEntry e) => _modelRe.firstMatch(e.msg)?.group(1);

  static String? _accountOf(LogEntry e) =>
      _accountRe.firstMatch(e.msg)?.group(1);

  /// 选项 = 配置列表 ∪ 日志里实际出现过的值,保持先配置后发现的顺序。
  List<String> _choices(List<String> known, String? Function(LogEntry) pick) {
    final seen = <String>{...known};
    final out = [...known];
    for (final e in _entries) {
      final v = pick(e);
      if (v != null && seen.add(v)) out.add(v);
    }
    return out;
  }

  @override
  void didUpdateWidget(LogsPage oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.active != widget.active) {
      _syncTimer();
    }
  }

  @override
  void dispose() {
    _timer?.cancel();
    _scroll.dispose();
    super.dispose();
  }

  void _syncTimer() {
    if (widget.active && _timer == null && _error == null) {
      _timer = Timer.periodic(_pollInterval, (_) => _poll());
    } else if (!widget.active && _timer != null) {
      _timer?.cancel();
      _timer = null;
    }
  }

  Future<void> _reload() async {
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final (entries, next) = await widget.client.fetchLogs(0);
      if (!mounted) return;
      setState(() {
        _entries
          ..clear()
          ..addAll(entries);
        _since = next;
        _loading = false;
      });
      _followToEnd();
      _syncTimer();
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e;
        _loading = false;
      });
    }
  }

  Future<void> _poll() async {
    try {
      final (entries, next) = await widget.client.fetchLogs(_since);
      if (!mounted) return;
      setState(() {
        _entries.addAll(entries);
        // 与后端环形缓冲同容量裁剪，界面不无限堆积。
        if (_entries.length > 2000) {
          _entries.removeRange(0, _entries.length - 2000);
        }
        _since = next;
      });
      _followToEnd();
    } catch (_) {
      // 轮询失败不打断展示：下一次再试，错误面板只在首载失败时出现。
    }
  }

  void _followToEnd() {
    if (!_follow) return;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || !_scroll.hasClients) return;
      _scroll.jumpTo(_scroll.position.maxScrollExtent);
    });
  }

  /// 用户滚离底部即暂停自动跟随（否则每 1.5s 被拽回尾部，翻不动历史）；
  /// 滚回底部恢复。阈值留 40px 吸收跳尾与行高取整的误差。
  void _onUserScroll() {
    if (!_scroll.hasClients) return;
    final p = _scroll.position;
    final atEnd = p.pixels >= p.maxScrollExtent - 40;
    if (_follow && !atEnd) {
      setState(() {
        _follow = false;
        _autoPaused = true;
      });
    } else if (!_follow && _autoPaused && atEnd) {
      setState(() {
        _follow = true;
        _autoPaused = false;
      });
    }
  }

  Future<void> _clear() async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => const ConfirmDialog(
        title: '清空日志',
        message: '将清空服务端缓冲中本次运行的日志，界面上已展示的内容一并消失。',
        confirmLabel: '清空',
        severity: ConfirmSeverity.warning,
      ),
    );
    if (ok != true) return;
    try {
      await widget.client.clearLogs();
      if (!mounted) return;
      setState(() {
        _entries.clear();
        _since = 0;
      });
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  List<LogEntry> get _filtered => _entries.where((e) {
        if (_level != 'all' && e.level != _level) return false;
        if (_modelFilter != 'all' && _modelOf(e) != _modelFilter) return false;
        if (_accountFilter != 'all' && _accountOf(e) != _accountFilter) {
          return false;
        }
        return true;
      }).toList();

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Column(
      children: [
        PageHeader(
          title: '日志',
          count: _filtered.length,
          trailing: [
            SizedBox(
              width: 172,
              child: StyledDropdown(
                value: _modelFilter,
                options: ['all', ..._choices(_knownModels, _modelOf)],
                labelOf: (o) => o == 'all' ? '全部模型' : o,
                onChanged: (v) => setState(() => _modelFilter = v ?? 'all'),
              ),
            ),
            const SizedBox(width: 8),
            SizedBox(
              width: 136,
              child: StyledDropdown(
                value: _accountFilter,
                options: ['all', ..._choices(_knownAccounts, _accountOf)],
                labelOf: (o) => o == 'all' ? '全部账号' : o,
                onChanged: (v) => setState(() => _accountFilter = v ?? 'all'),
              ),
            ),
            const SizedBox(width: 8),
            SizedBox(
              width: 116,
              child: StyledDropdown(
                value: _level,
                options: _levelOptions,
                labelOf: (o) => _levelLabels[o] ?? o,
                onChanged: (v) => setState(() => _level = v ?? 'all'),
              ),
            ),
            const SizedBox(width: 8),
            Tooltip(
              message: _follow ? '自动跟随：开（点击释放）' : '自动跟随：关（点击激活）',
              child: OutlinedButton(
                style: OutlinedButton.styleFrom(
                  foregroundColor: _follow ? t.primaryInk : null,
                ),
                onPressed: () {
                  setState(() {
                    _follow = !_follow;
                    _autoPaused = false; // 手动开关优先，不被自动恢复覆盖
                  });
                  if (_follow) _followToEnd();
                },
                child: Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    Icon(
                      _follow
                          ? Icons.vertical_align_bottom_rounded
                          : Icons.pause_circle_outline_rounded,
                      size: 15,
                    ),
                    const SizedBox(width: 6),
                    const Text('跟随'),
                  ],
                ),
              ),
            ),
            const SizedBox(width: 8),
            OutlinedButton(
              onPressed: _clear,
              child: const Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(Icons.delete_sweep_outlined, size: 15),
                  SizedBox(width: 6),
                  Text('清空'),
                ],
              ),
            ),
          ],
        ),
        Expanded(
          child: Padding(
            padding: const EdgeInsets.fromLTRB(24, 0, 24, 20),
            child: _body(t),
          ),
        ),
      ],
    );
  }

  Widget _body(AppTokens t) {
    if (_loading) {
      return const Center(child: CircularProgressIndicator());
    }
    if (_error != null) {
      return ErrorPanel(
        error: _error!,
        onRetry: _reload,
        onOpenSettings: widget.onOpenSettings,
      );
    }
    final rows = _filtered;
    if (rows.isEmpty) {
      return Center(
        child: Text(
          _entries.isEmpty ? '暂无日志条目，服务产生事件后会自动出现' : '当前筛选条件下暂无日志',
          style: TextStyle(fontSize: 13, color: t.faint),
        ),
      );
    }
    return Card(
      clipBehavior: Clip.antiAlias,
      child: Column(
        children: [
          _columnHead(t),
          const Divider(height: 1),
          Expanded(
            child: ListView.separated(
              controller: _scroll,
              padding: const EdgeInsets.symmetric(vertical: 6),
              itemCount: rows.length,
              separatorBuilder: (_, _) =>
                  Divider(height: 1, indent: 16, endIndent: 16),
              itemBuilder: (_, i) => _row(t, rows[i]),
            ),
          ),
        ],
      ),
    );
  }

  Widget _columnHead(AppTokens t) {
    const head = TextStyle(fontSize: 11, fontWeight: FontWeight.w600);
    return Container(
      padding: const EdgeInsets.fromLTRB(16, 8, 16, 8),
      color: t.bg,
      child: Row(
        children: [
          SizedBox(width: _colTime, child: Text('时间', style: head)),
          SizedBox(width: _colGap),
          SizedBox(width: _colLevel, child: Text('级别', style: head)),
          SizedBox(width: _colGap),
          SizedBox(width: _colSource, child: Text('来源', style: head)),
          SizedBox(width: _colGap),
          Text('内容', style: head),
        ],
      ),
    );
  }

  Widget _row(AppTokens t, LogEntry e) {
    final at = e.at.toLocal();
    final time =
        '${at.hour.toString().padLeft(2, '0')}:${at.minute.toString().padLeft(2, '0')}:${at.second.toString().padLeft(2, '0')}';
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 5, 16, 5),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: _colTime,
            child: Text(
              time,
              style: TextStyle(
                fontSize: 11.5,
                color: t.faint,
                fontFamily: AppConst.fontMono,
              ),
            ),
          ),
          const SizedBox(width: _colGap),
          SizedBox(width: _colLevel, child: _levelChip(t, e.level)),
          const SizedBox(width: _colGap),
          SizedBox(
            width: _colSource,
            child: Text(
              e.source,
              overflow: TextOverflow.ellipsis,
              style: TextStyle(
                fontSize: 11.5,
                color: t.dim,
                fontFamily: AppConst.fontMono,
              ),
            ),
          ),
          const SizedBox(width: _colGap),
          Expanded(
            child: SelectableText(
              e.msg,
              style: TextStyle(fontSize: 12.5, color: t.ink, height: 1.45),
            ),
          ),
        ],
      ),
    );
  }

  Widget _levelChip(AppTokens t, String level) {
    final (bg, fg) = switch (level) {
      'warn' => (t.warn.withValues(alpha: .14), t.warn),
      'error' => (t.dangerSoft, t.danger),
      _ => (t.bg, t.dim),
    };
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 2),
      decoration: BoxDecoration(
        color: bg,
        borderRadius: BorderRadius.circular(6),
      ),
      child: Text(
        level,
        style: TextStyle(
          fontSize: 10.5,
          fontWeight: FontWeight.w600,
          color: fg,
          fontFamily: AppConst.fontMono,
        ),
      ),
    );
  }
}
