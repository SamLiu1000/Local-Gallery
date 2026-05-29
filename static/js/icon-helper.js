/* ============================================================
   icon-helper.js - 图标辅助函数
   替代 Emoji 图标，使用 SVG mask 实现主题自适应
   ============================================================ */

const Icons = (() => {
    /**
     * 返回图标 <span> 的 HTML 字符串
     * @param {string} name - 图标名称
     * @returns {string}
     */
    function span(name) {
        return `<span class="icon icon-${name}"></span>`;
    }

    /**
     * 返回图标 + 文本的 HTML 字符串
     * @param {string} name - 图标名称
     * @param {string} [text] - 紧跟的文本
     * @returns {string}
     */
    function get(name, text) {
        if (text !== undefined) {
            return span(name) + ' ' + text;
        }
        return span(name);
    }

    /**
     * 创建图标 DOM 元素（用于 textContent 替代场景）
     * @param {string} name - 图标名称
     * @param {string} [text] - 紧跟的文本节点
     * @returns {DocumentFragment}
     */
    function frag(name, text) {
        const f = document.createDocumentFragment();
        const s = document.createElement('span');
        s.className = 'icon icon-' + name;
        f.appendChild(s);
        if (text !== undefined) {
            f.appendChild(document.createTextNode(' ' + text));
        }
        return f;
    }

    /**
     * 为元素设置图标 + 文本内容（替代 textContent）
     * @param {HTMLElement} el
     * @param {string} name - 图标名称
     * @param {string} [text] - 文本
     */
    function set(el, name, text) {
        el.innerHTML = '';
        el.appendChild(frag(name, text));
    }

    return { span, get, frag, set };
})();
