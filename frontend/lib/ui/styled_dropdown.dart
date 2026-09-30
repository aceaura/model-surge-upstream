import 'package:flutter/material.dart';

import '../theme.dart';

/// 移植自 fengshen-slicer 的自绘下拉:触发器复用输入框外壳(labelText 与
/// TextField 同款浮动 label:showValue=false 时占位显示在框内,打开菜单或选中
/// 子项后浮到框缘),菜单落在触发器正下方(间距 6,不遮挡触发器),surface 底
/// radius 12 + 1px 浅描边 + 投影,内缩 primarySoft 圆角 8 选中块 +
/// primaryInk 加粗 + 主色 check。
class StyledDropdown extends StatefulWidget {
  final String? value;
  final List<String> options;
  final ValueChanged<String?> onChanged;
  final InputDecoration? decoration;
  final String Function(String)? labelOf;
  // false=未选占位态:框内显示 label,值隐藏;true=显示值,label 浮在框缘
  final bool showValue;
  const StyledDropdown(
      {super.key,
      required this.value,
      required this.options,
      required this.onChanged,
      this.decoration,
      this.labelOf,
      this.showValue = true});

  @override
  State<StyledDropdown> createState() => _StyledDropdownState();
}

class _StyledDropdownState extends State<StyledDropdown> {
  final LayerLink _link = LayerLink();
  final GlobalKey _triggerKey = GlobalKey();
  OverlayEntry? _entry;

  String _label(String o) => widget.labelOf == null ? o : widget.labelOf!(o);

  bool get _open => _entry != null;

  void _toggle() => _open ? _close() : _show();

  void _show() {
    final box = _triggerKey.currentContext!.findRenderObject() as RenderBox;
    final t = context.tokens;
    final dark = Theme.of(context).brightness == Brightness.dark;
    _entry = OverlayEntry(
      builder: (_) => Stack(children: [
        // 透明屏障:点外部收菜单
        Positioned.fill(
          child: GestureDetector(
            onTap: _close,
            behavior: HitTestBehavior.translucent,
            child: const SizedBox.expand(),
          ),
        ),
        CompositedTransformFollower(
          link: _link,
          showWhenUnlinked: false,
          offset: Offset(0, box.size.height + 6),
          child: Align(
            alignment: Alignment.topLeft,
            // 底色必须画在 Material 上:InkWell 的 hover 墨水绘在最近 Material 层,
            // 若底色由子级 Container 画会盖住墨水,悬停永远不可见
            child: Container(
              width: box.size.width,
              constraints: const BoxConstraints(maxHeight: 360),
              decoration: BoxDecoration(
                borderRadius: BorderRadius.circular(12),
                border: Border.all(
                    color: dark ? t.border : const Color(0xFFE3E7EE)),
                boxShadow: [
                  BoxShadow(
                    color: dark
                        ? Colors.black54
                        : const Color.fromRGBO(20, 30, 60, .16),
                    blurRadius: 24,
                    offset: const Offset(0, 6),
                  ),
                ],
              ),
              child: Material(
                color: t.surface,
                borderRadius: BorderRadius.circular(12),
                clipBehavior: Clip.antiAlias,
                child: Padding(
                  padding: const EdgeInsets.all(6),
                  child: SingleChildScrollView(
                    child: Column(
                      mainAxisSize: MainAxisSize.min,
                      children: [for (final o in widget.options) _item(t, o)],
                    ),
                  ),
                ),
              ),
            ),
          ),
        ),
      ]),
    );
    Overlay.of(context).insert(_entry!);
    setState(() {}); // _open 转真:聚焦描边与浮标(InputDecorator 自带过渡)
  }

  Widget _item(AppTokens t, String o) {
    final sel = o == widget.value;
    // 上下各 2px 空隙:相邻选中/悬停色块不互贴
    return SizedBox(
      height: 38,
      child: Padding(
        padding: const EdgeInsets.symmetric(vertical: 2),
        child: InkResponse(
          borderRadius: BorderRadius.circular(8),
          highlightShape: BoxShape.rectangle,
          hoverColor: t.primarySoft,
          splashColor: Colors.transparent,
          highlightColor: Colors.transparent,
          onTap: () {
            _close();
            widget.onChanged(o);
          },
          child: Container(
            padding: const EdgeInsets.symmetric(horizontal: 10),
            decoration: sel
                ? BoxDecoration(
                    color: t.primarySoft, borderRadius: BorderRadius.circular(8))
                : null,
            child: Row(children: [
              Text(_label(o),
                  style: TextStyle(
                    fontSize: 13,
                    color: sel ? t.primaryInk : t.ink,
                    fontWeight: sel ? FontWeight.w600 : null,
                    fontFamily: AppConst.fontFamily,
                  )),
              const Spacer(),
              if (sel) Icon(Icons.check, size: 15, color: t.primary),
            ]),
          ),
        ),
      ),
    );
  }

  void _close() {
    if (_entry == null) return;
    _entry!.remove();
    _entry = null;
    setState(() {}); // 触发器聚焦描边与浮标随菜单开合还原
  }

  @override
  void dispose() {
    _entry?.remove();
    _entry = null;
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    final d = widget.decoration ?? const InputDecoration();
    // label 交给 InputDecorator 原生浮动(与 TextField 同机制:字号/居中/缺口/
    // 高度全自动一致)。showValue=false 视为空(占位 label 在框内);打开菜单
    // (isFocused)或已选子项(showValue=true)时浮到框缘
    final showContent = widget.showValue || _open;
    final current = widget.value;
    return CompositedTransformTarget(
      link: _link,
      child: MouseRegion(
        cursor: SystemMouseCursors.click,
        child: GestureDetector(
          key: _triggerKey,
          behavior: HitTestBehavior.opaque,
          onTap: _toggle,
          child: InputDecorator(
            decoration: d,
            isEmpty: !widget.showValue,
            isFocused: _open,
            // 定高:值显隐切换时盒子不缩;字号与输入框正文一致(13)
            child: SizedBox(
              height: 19,
              // 箭头常驻(含占位态);只值文本按态显隐
              child: Row(children: [
                Expanded(
                  child: !showContent
                      ? const SizedBox.shrink()
                      : Text(
                          current == null ? '' : _label(current),
                          overflow: TextOverflow.ellipsis,
                          // 显式钉字体族:textStyle 缺 fontFamily 会在合并链上丢掉雅黑
                          style: TextStyle(
                              fontSize: 13,
                              color: t.ink,
                              fontFamily: AppConst.fontFamily),
                        ),
                ),
                Icon(Icons.expand_more_rounded, size: 18, color: t.dim),
              ]),
            ),
          ),
        ),
      ),
    );
  }
}

/// 带 Form 校验的 StyledDropdown:errorText 走 InputDecorator 原生错误描边/文案。
class StyledDropdownFormField extends FormField<String> {
  StyledDropdownFormField({
    super.key,
    required String? value,
    required List<String> options,
    required ValueChanged<String?> onChanged,
    InputDecoration decoration = const InputDecoration(),
    String Function(String)? labelOf,
    super.validator,
  }) : super(
          initialValue: value,
          builder: (field) => StyledDropdown(
            value: field.value,
            options: options,
            labelOf: labelOf,
            showValue: field.value != null,
            decoration: decoration.copyWith(errorText: field.errorText),
            onChanged: (v) {
              field.didChange(v);
              onChanged(v);
            },
          ),
        );
}
