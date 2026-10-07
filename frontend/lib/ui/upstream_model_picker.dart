import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';

/// 给既有上游模型输入框附加临时选择器；只向表单返回模型 ID。
class UpstreamModelPicker extends StatefulWidget {
  const UpstreamModelPicker({
    super.key,
    required this.client,
    required this.account,
    required this.value,
    required this.onSelected,
    required this.child,
    this.enabled = true,
  });

  final ApiClient client;
  final String account;
  final String value;
  final ValueChanged<String> onSelected;
  final Widget child;
  final bool enabled;

  @override
  State<UpstreamModelPicker> createState() => _UpstreamModelPickerState();
}

class _UpstreamModelPickerState extends State<UpstreamModelPicker> {
  static const _rowHeight = 36.0;
  static const _headerHeight = 56.0;
  static const _searchHeight = 36.0;
  static const _footerHeight = 28.0;
  final _portal = OverlayPortalController();
  final _tapGroup = Object();
  final _search = TextEditingController();
  final _scroll = ScrollController();
  final _searchFocus = FocusNode(debugLabel: 'Upstream model search');
  final _panelFocus = FocusNode(debugLabel: 'Upstream model panel');
  final _fetchFocus = FocusNode(debugLabel: 'Upstream model fetch');
  UpstreamModelListing? _listing;
  bool _open = false;
  bool _loading = false;
  bool _failed = false;
  int _generation = 0;
  int _active = -1;

  bool get _canFetch => widget.enabled && widget.account.trim().isNotEmpty;

  List<UpstreamModelOption> get _filtered {
    final models = _listing?.models ?? const <UpstreamModelOption>[];
    final query = _search.text.trim().toLowerCase();
    if (query.isEmpty) return models;
    return models
        .where(
          (option) =>
              option.id.toLowerCase().contains(query) ||
              option.displayName.toLowerCase().contains(query),
        )
        .toList();
  }

  @override
  void didUpdateWidget(covariant UpstreamModelPicker oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.account != widget.account ||
        !identical(oldWidget.client, widget.client) ||
        !_canFetch) {
      // 父表单换账号、客户端或禁用时，丢弃整次查询，而非沿用旧列表。
      _invalidate(afterFrame: true);
    } else if (oldWidget.value != widget.value) {
      _active = -1;
    }
  }

  void _invalidate({bool afterFrame = false}) {
    final generation = ++_generation;
    final wasOpen = _open;
    _open = false;
    _loading = false;
    _failed = false;
    _listing = null;
    _active = -1;
    void clearOverlay() {
      if (!mounted || _open || generation != _generation) return;
      if (wasOpen) {
        _portal.hide();
        _panelFocus.unfocus();
        _searchFocus.unfocus();
      }
      _search.clear();
    }

    if (afterFrame) {
      WidgetsBinding.instance.addPostFrameCallback((_) => clearOverlay());
    } else {
      clearOverlay();
    }
  }

  void _close({bool restoreFocus = false}) {
    if (!_open) return;
    setState(_invalidate);
    if (restoreFocus && _canFetch) _fetchFocus.requestFocus();
  }

  bool _isCurrent(int generation, ApiClient client, String account) =>
      mounted &&
      _open &&
      generation == _generation &&
      identical(client, widget.client) &&
      account == widget.account;

  void _focusAfterFrame(int generation, {required bool search}) {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || !_open || generation != _generation) return;
      (search ? _searchFocus : _panelFocus).requestFocus();
    });
  }

  Future<void> _fetch() async {
    if (!_canFetch || _loading) return;
    final generation = ++_generation;
    final client = widget.client;
    final account = widget.account;
    setState(() {
      _open = true;
      _loading = true;
      _failed = false;
      _listing = null;
      _active = -1;
      _search.clear();
    });
    _portal.show();
    _focusAfterFrame(generation, search: false);
    try {
      final result = await client.listUpstreamModels(account);
      if (!_isCurrent(generation, client, account)) return;
      setState(() {
        _listing = result;
        _loading = false;
      });
      _focusAfterFrame(
        generation,
        search: result.queryable && result.models.isNotEmpty,
      );
    } catch (_) {
      if (!_isCurrent(generation, client, account)) return;
      // 不展示未经筛选的后端错误（可能含凭据），不改输入框原值。
      setState(() {
        _loading = false;
        _failed = true;
      });
      _focusAfterFrame(generation, search: false);
    }
  }

  void _select(UpstreamModelOption option) {
    _close(restoreFocus: true);
    widget.onSelected(option.id);
  }

  void _filterChanged(String _) {
    setState(() => _active = -1);
    if (_scroll.hasClients) _scroll.jumpTo(0);
  }

  void _clearSearch() {
    _search.clear();
    _filterChanged('');
    _searchFocus.requestFocus();
  }

  KeyEventResult _onEscape(FocusNode node, KeyEvent event) {
    if (_open &&
        (event is KeyDownEvent || event is KeyRepeatEvent) &&
        event.logicalKey == LogicalKeyboardKey.escape) {
      _close(restoreFocus: true);
      return KeyEventResult.handled;
    }
    return KeyEventResult.ignored;
  }

  KeyEventResult _onKey(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent && event is! KeyRepeatEvent) {
      return KeyEventResult.ignored;
    }
    if (event.logicalKey == LogicalKeyboardKey.escape) {
      _close(restoreFocus: true);
      return KeyEventResult.handled;
    }
    final options = _filtered;
    if (_loading || _failed || _listing?.queryable != true || options.isEmpty) {
      return KeyEventResult.ignored;
    }
    if (event.logicalKey == LogicalKeyboardKey.arrowDown ||
        event.logicalKey == LogicalKeyboardKey.arrowUp) {
      final next = event.logicalKey == LogicalKeyboardKey.arrowDown
          ? math.min(_active + 1, options.length - 1)
          : math.max(_active - 1, 0);
      setState(() => _active = next);
      if (_scroll.hasClients) {
        final position = _scroll.position;
        final top = 6 + next * _rowHeight;
        final bottom = top + _rowHeight;
        final offset = top < position.pixels
            ? top
            : bottom > position.pixels + position.viewportDimension
            ? bottom - position.viewportDimension
            : position.pixels;
        _scroll.jumpTo(offset.clamp(0.0, position.maxScrollExtent));
      }
      return KeyEventResult.handled;
    }
    if (event.logicalKey == LogicalKeyboardKey.enter ||
        event.logicalKey == LogicalKeyboardKey.numpadEnter) {
      _select(options[_active < 0 ? 0 : _active]);
      return KeyEventResult.handled;
    }
    return KeyEventResult.ignored;
  }

  @override
  void dispose() {
    ++_generation;
    _search.dispose();
    _scroll.dispose();
    _searchFocus.dispose();
    _panelFocus.dispose();
    _fetchFocus.dispose();
    // OverlayPortal 自身随表单移除；不在 dispose 内更新 overlay 或 setState。
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Focus(
      onKeyEvent: _onEscape,
      child: OverlayPortal(
        controller: _portal,
        overlayChildBuilder: _buildOverlay,
        child: TapRegion(
          groupId: _tapGroup,
          onTapOutside: (_) => _close(),
          child: IntrinsicHeight(
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                Expanded(child: widget.child),
                const SizedBox(width: 8),
                OutlinedButton.icon(
                  key: const ValueKey('upstream-model-fetch'),
                  focusNode: _fetchFocus,
                  onPressed: _canFetch && !_loading ? _fetch : null,
                  style: OutlinedButton.styleFrom(
                    backgroundColor: t.primarySoft,
                    foregroundColor: t.primaryInk,
                    disabledBackgroundColor: t.primarySoft,
                    disabledForegroundColor: t.faint,
                    side: BorderSide(color: t.primary.withValues(alpha: .30)),
                    padding: const EdgeInsets.symmetric(horizontal: 12),
                    minimumSize: const Size(0, 48),
                    tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                  ),
                  icon: const Icon(Icons.refresh_rounded, size: 16),
                  label: const Text('获取模型'),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  Widget _buildOverlay(BuildContext context) {
    if (!_open) return const SizedBox.shrink();
    return MediaQuery.fromView(
      view: View.of(context),
      child: LayoutBuilder(
        builder: (context, constraints) {
          final media = MediaQuery.of(context);
          final insets = EdgeInsets.fromLTRB(
            media.padding.left + 16,
            media.padding.top + 16,
            media.padding.right + 16,
            math.max(media.padding.bottom, media.viewInsets.bottom) + 16,
          );
          final width = math.min(
            480.0,
            math.max(0.0, constraints.maxWidth - insets.horizontal),
          );
          final options = _filtered;
          final hasSearch =
              _listing?.queryable == true &&
              (_listing?.models.isNotEmpty ?? false);
          final desired = hasSearch
              ? (140.0 + _listing!.models.length * _rowHeight).clamp(
                  240.0,
                  440.0,
                )
              : 240.0;
          final height = math.min(
            desired,
            math.max(0.0, constraints.maxHeight - insets.vertical),
          );
          if (width <= 0 || height <= 0) return const SizedBox.shrink();
          return Padding(
            padding: insets,
            child: Center(
              child: TapRegion(
                groupId: _tapGroup,
                onTapOutside: (_) => _close(),
                child: Focus(
                  focusNode: _panelFocus,
                  onKeyEvent: _onKey,
                  child: SizedBox(
                    width: width,
                    height: height,
                    child: _panel(context, options, hasSearch, height),
                  ),
                ),
              ),
            ),
          );
        },
      ),
    );
  }

  Widget _panel(
    BuildContext context,
    List<UpstreamModelOption> options,
    bool hasSearch,
    double height,
  ) {
    final t = context.tokens;
    final dark = Theme.of(context).brightness == Brightness.dark;
    final compact = height < 220;
    final showSearch = hasSearch && height >= 180;
    return DecoratedBox(
      decoration: BoxDecoration(
        borderRadius: BorderRadius.circular(12),
        border: Border.all(color: t.border),
        boxShadow: [
          BoxShadow(
            color: t.ink.withValues(alpha: dark ? .24 : .12),
            blurRadius: 24,
            offset: const Offset(0, 6),
          ),
        ],
      ),
      child: Material(
        key: const ValueKey('upstream-model-panel'),
        color: t.surface,
        borderRadius: BorderRadius.circular(12),
        clipBehavior: Clip.antiAlias,
        child: Column(
          children: [
            SizedBox(
              height: math.min(compact ? 44.0 : _headerHeight, height * .35),
              child: Padding(
                padding: const EdgeInsets.symmetric(horizontal: 12),
                child: Row(
                  children: [
                    Expanded(
                      child: Column(
                        mainAxisAlignment: MainAxisAlignment.center,
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            '选择上游模型',
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: TextStyle(
                              fontSize: 14,
                              fontWeight: FontWeight.w600,
                              color: t.ink,
                            ),
                          ),
                          if (!compact)
                            Text(
                              '${widget.account} · ${_listing == null ? (_loading ? '获取中…' : '获取失败') : '${_listing!.models.length} 个模型'}',
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: TextStyle(fontSize: 11, color: t.dim),
                            ),
                        ],
                      ),
                    ),
                    _iconButton(
                      'upstream-model-refresh',
                      '重新获取模型列表',
                      Icons.refresh_rounded,
                      _loading ? null : _fetch,
                    ),
                    _iconButton(
                      'upstream-model-close',
                      '关闭模型列表',
                      Icons.close_rounded,
                      () => _close(restoreFocus: true),
                    ),
                  ],
                ),
              ),
            ),
            if (showSearch)
              Padding(
                padding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
                child: SizedBox(
                  height: _searchHeight,
                  child: TextField(
                    key: const ValueKey('upstream-model-search'),
                    controller: _search,
                    focusNode: _searchFocus,
                    onChanged: _filterChanged,
                    style: TextStyle(fontSize: 13, color: t.ink),
                    decoration: InputDecoration(
                      hintText: '搜索模型名…',
                      prefixIcon: Icon(Icons.search, size: 17, color: t.dim),
                      prefixIconConstraints: const BoxConstraints(
                        minWidth: 36,
                        minHeight: 36,
                      ),
                      suffixIcon: _search.text.isEmpty
                          ? null
                          : _iconButton(
                              'upstream-model-clear',
                              '清空搜索',
                              Icons.close_rounded,
                              _clearSearch,
                            ),
                      suffixIconConstraints: const BoxConstraints(
                        minWidth: 32,
                        minHeight: 32,
                      ),
                      contentPadding: const EdgeInsets.symmetric(
                        horizontal: 10,
                        vertical: 8,
                      ),
                    ),
                  ),
                ),
              ),
            Expanded(child: _body(context, options)),
            SizedBox(
              height: math.min(_footerHeight, height * .15),
              child: DecoratedBox(
                decoration: BoxDecoration(
                  border: Border(top: BorderSide(color: t.border)),
                ),
                child: Padding(
                  padding: const EdgeInsets.symmetric(horizontal: 12),
                  child: LayoutBuilder(
                    builder: (context, constraints) {
                      return Row(
                        children: [
                          Expanded(
                            child: Text(
                              _count(options.length),
                              maxLines: 1,
                              overflow: TextOverflow.ellipsis,
                              style: TextStyle(fontSize: 11, color: t.dim),
                            ),
                          ),
                          if (constraints.maxWidth >= 400)
                            Text(
                              '↑ ↓ 浏览  Enter 选择  Esc 关闭',
                              style: TextStyle(fontSize: 10, color: t.faint),
                            ),
                        ],
                      );
                    },
                  ),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }

  Widget _iconButton(
    String key,
    String tooltip,
    IconData icon,
    VoidCallback? onPressed,
  ) {
    return IconButton(
      key: ValueKey(key),
      tooltip: tooltip,
      onPressed: onPressed,
      icon: Icon(icon, size: 17),
      color: context.tokens.dim,
      padding: EdgeInsets.zero,
      constraints: const BoxConstraints.tightFor(width: 32, height: 32),
      style: IconButton.styleFrom(
        tapTargetSize: MaterialTapTargetSize.shrinkWrap,
      ),
    );
  }

  String _count(int matches) {
    if (_loading) return '获取中…';
    if (_failed || _listing?.queryable != true || _listing!.models.isEmpty) {
      return '原有模型名未更改';
    }
    if (_search.text.trim().isNotEmpty) {
      return '找到 $matches / ${_listing!.models.length} 个模型';
    }
    return '共 ${_listing!.models.length} 个模型 · 点击即填入';
  }

  Widget _body(BuildContext context, List<UpstreamModelOption> options) {
    if (_loading) {
      return _message(
        context,
        '正在获取模型列表',
        '请稍候，获取完成后可以搜索或滚动选择。',
        loading: true,
      );
    }
    if (_failed) {
      return _message(
        context,
        '获取模型列表失败',
        '请检查当前账号的凭据和连接后重试；也可以手动输入模型名。',
        failure: true,
        retry: true,
      );
    }
    if (_listing?.queryable != true) {
      return _message(context, '当前账号不支持查询模型列表', '你仍可以手动输入上游模型名。');
    }
    if (_listing!.models.isEmpty) {
      return _message(
        context,
        '上游未返回可选模型',
        '当前账号暂无模型列表；你仍可以手动输入模型名。',
        retry: true,
      );
    }
    if (options.isEmpty) {
      return _message(context, '没有匹配的模型', '换个关键词试试，或清空搜索浏览全部模型。');
    }
    final t = context.tokens;
    return Scrollbar(
      controller: _scroll,
      thumbVisibility: true,
      child: ListView.builder(
        key: const ValueKey('upstream-model-list'),
        controller: _scroll,
        padding: const EdgeInsets.all(6),
        itemExtent: _rowHeight,
        itemCount: options.length,
        itemBuilder: (context, index) {
          final option = options[index];
          final selected = option.id == widget.value;
          return Padding(
            padding: const EdgeInsets.symmetric(vertical: 2),
            child: Material(
              color: selected || index == _active ? t.primarySoft : t.surface,
              borderRadius: BorderRadius.circular(8),
              child: Tooltip(
                message: option.displayName.isEmpty
                    ? option.id
                    : '${option.id}\n${option.displayName}',
                child: Semantics(
                  selected: selected,
                  button: true,
                  child: InkWell(
                    key: ValueKey('upstream-model-option-${option.id}'),
                    borderRadius: BorderRadius.circular(8),
                    hoverColor: t.primarySoft,
                    onTap: () => _select(option),
                    child: Padding(
                      padding: const EdgeInsets.symmetric(horizontal: 10),
                      child: Row(
                        children: [
                          Expanded(
                            child: Column(
                              mainAxisAlignment: MainAxisAlignment.center,
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                Text(
                                  option.id,
                                  maxLines: 1,
                                  overflow: TextOverflow.ellipsis,
                                  style: TextStyle(
                                    fontFamily: AppConst.fontMono,
                                    fontSize: 13,
                                    height: 1.2,
                                    fontWeight: selected
                                        ? FontWeight.w600
                                        : FontWeight.w400,
                                    color: selected ? t.primaryInk : t.ink,
                                  ),
                                ),
                                if (option.displayName.isNotEmpty &&
                                    option.displayName != option.id)
                                  Text(
                                    option.displayName,
                                    maxLines: 1,
                                    overflow: TextOverflow.ellipsis,
                                    style: TextStyle(
                                      fontSize: 11,
                                      height: 1.2,
                                      color: t.dim,
                                    ),
                                  ),
                              ],
                            ),
                          ),
                          if (selected) ...[
                            const SizedBox(width: 8),
                            Icon(
                              Icons.check_rounded,
                              size: 17,
                              color: t.primary,
                            ),
                          ],
                        ],
                      ),
                    ),
                  ),
                ),
              ),
            ),
          );
        },
      ),
    );
  }

  Widget _message(
    BuildContext context,
    String title,
    String detail, {
    bool loading = false,
    bool failure = false,
    bool retry = false,
  }) {
    final t = context.tokens;
    // 状态提示也可滚动：极矮视口仍能访问重试，不造成 Column overflow。
    return Center(
      child: SingleChildScrollView(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 16),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (loading)
              const SizedBox(
                width: 22,
                height: 22,
                child: CircularProgressIndicator(strokeWidth: 2),
              )
            else
              Icon(
                failure ? Icons.error_outline : Icons.search_rounded,
                size: 24,
                color: failure ? t.danger : t.faint,
              ),
            const SizedBox(height: 10),
            Text(
              title,
              textAlign: TextAlign.center,
              style: TextStyle(
                fontSize: 13,
                fontWeight: FontWeight.w600,
                color: failure ? t.dangerInk : t.ink,
              ),
            ),
            const SizedBox(height: 6),
            Text(
              detail,
              textAlign: TextAlign.center,
              style: TextStyle(fontSize: 12, color: t.dim),
            ),
            if (retry) ...[
              const SizedBox(height: 12),
              OutlinedButton(onPressed: _fetch, child: const Text('重新获取')),
            ],
          ],
        ),
      ),
    );
  }
}
