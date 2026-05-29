/* ============================================================
   detail.js - 右侧详情面板
   大图预览、参数展示、提示词版本管理、标签与收藏
   ============================================================ */

const ImageContextMenu = (() => {
    const t = (typeof I18n !== "undefined" ? I18n.t : (s) => s);
    let menu, btnOpen, btnTrack;

    function init() {
        const _t = (typeof I18n !== "undefined" ? I18n.t : (s) => s);
        menu = document.getElementById('imageContextMenu');
        btnOpen = document.getElementById('ctxOpenInFolder');
        btnTrack = document.getElementById('ctxTrackFolder');
        if (!menu || !btnOpen || menu._initialized) return;
        menu._initialized = true;

        btnOpen.addEventListener('click', async () => {
            if (btnOpen._action === 'refresh') {
                hide();
                try {
                    if (typeof WailsBridge !== 'undefined' && WailsBridge.refreshAll) {
                        await WailsBridge.refreshAll();
                    }
                    if (typeof Sidebar !== 'undefined' && Sidebar.refreshFolderTree) {
                        await Sidebar.refreshFolderTree();
                    }
                    if (typeof Gallery !== 'undefined') {
                        const ctx = Gallery.getCurrentContext();
                        if (ctx && ctx.folderPath) {
                            await Gallery.filterByFolder(ctx.folderPath, null, { forceRefresh: true });
                        }
                    }
                } catch (e) { /* 静默 */ }
                return;
            }
            const filePath = menu._currentFilePath;
            hide();
            if (filePath) {
                await openFileLocation(filePath);
            }
        });

        if (btnTrack) {
            btnTrack.addEventListener('click', async () => {
                const filePath = menu._currentFilePath;
                const rootPath = menu._currentRootPath;
                const folder = menu._currentFolder;
                hide();
                if (typeof Sidebar !== 'undefined' && Sidebar.navigateToFolder) {
                    await Sidebar.navigateToFolder(filePath, rootPath, folder);
                }
            });
        }

        document.addEventListener('click', (e) => {
            if (!menu.contains(e.target)) {
                hide();
            }
        });

        // 页面全局右键菜单：非图片区域只显示”刷新”
        document.addEventListener('contextmenu', (e) => {
            if (e.defaultPrevented) return;
            if (e.target.closest('img, .image-card, #imageViewerWrapper')) return;
            // ★ 按住 Ctrl 或 Shift 时放行浏览器原生右键菜单
            if (e.ctrlKey || e.shiftKey) return;
            e.preventDefault();
            showGeneral(e.clientX, e.clientY);
        });
    }

    function setImageMode() {
        if (btnOpen) {
            btnOpen.innerHTML = '<span class="icon icon-browse"></span> ' + t('detail.open_in_folder');
            btnOpen._action = 'file';
        }
    }

    function setRefreshMode() {
        if (btnOpen) {
            btnOpen.innerHTML = '<span class="icon icon-refresh"></span> ' + t('detail.refresh_folder');
            btnOpen._action = 'refresh';
        }
    }

    function show(x, y, filePath, rootPath, folder) {
        if (!menu) { init(); menu = document.getElementById('imageContextMenu'); }
        if (!menu) return;
        setImageMode();
        menu._currentFilePath = filePath;
        menu._currentRootPath = rootPath || null;
        menu._currentFolder = folder || null;
        menu.style.display = 'block';
        menu.style.left = x + 'px';
        menu.style.top = y + 'px';
        requestAnimationFrame(() => {
            const rect = menu.getBoundingClientRect();
            if (rect.right > window.innerWidth) {
                menu.style.left = (x - rect.width) + 'px';
            }
            if (rect.bottom > window.innerHeight) {
                menu.style.top = (y - rect.height) + 'px';
            }
        });
    }

    function showGeneral(x, y) {
        if (!menu) { init(); menu = document.getElementById('imageContextMenu'); }
        if (!menu) return;
        setRefreshMode();
        menu._currentFilePath = null;
        menu.style.display = 'block';
        menu.style.left = x + 'px';
        menu.style.top = y + 'px';
        requestAnimationFrame(() => {
            const rect = menu.getBoundingClientRect();
            if (rect.right > window.innerWidth) {
                menu.style.left = (x - rect.width) + 'px';
            }
            if (rect.bottom > window.innerHeight) {
                menu.style.top = (y - rect.height) + 'px';
            }
        });
    }

    function hide() {
        if (menu) {
            menu.style.display = 'none';
            menu._currentFilePath = null;
            menu._currentRootPath = null;
            menu._currentFolder = null;
        }
    }

    async function openFileLocation(filePath) {
        if (!filePath) {
            App.showToast(_t('detail.no_file_path'), 'warning');
            return;
        }
        try {
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                await WailsBridge.openFileLocation(filePath);
                App.showToast(_t('detail.file_located'), 'success');
            } else {
                const response = await fetch('/api/open-file', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ path: filePath })
                });
                const result = await response.json();
                if (result.success) {
                    App.showToast(_t('detail.file_located'), 'success');
                } else {
                    App.showToast(_t('detail.locate_failed') + ': ' + (result.error || _t('detail.unknown')), 'error');
                }
            }
        } catch (err) {
            App.showToast(_t('detail.request_failed') + ': ' + err.message, 'error');
        }
    }

    // 模块加载时立即初始化（此时 DOM 元素已就绪）
    init();

    return { show, hide, openFileLocation, init };
})();

const DetailPanel = (() => {
    const _t = (typeof I18n !== "undefined" ? I18n.t : (s) => s);
    // DOM
    let rightPanel, toggleBtn, detailContent;
    let detailPlaceholder, detailInfo;
    let detailImage, detailPreview;
    let fileInfo, positivePrompt, negativePrompt;
    let paramsGrid, imageTags, tagSelect;
    let btnToggleFavorite, btnAddTag;
    let btnPrevPrompt, btnNextPrompt, btnDeletePrompt, btnAddPrompt;
    let promptVersionLabel;
    let btnGeneratePrompt, btnStopGenerate, detailApiConfigSelect;
    let btnToggleRawMetadata, btnCopyRawMetadata, btnCloseRawMetadata, rawMetadataPanel, rawMetadataList;

    // 状态
    let currentImage = null;
    let generateAbortController = null;
    let currentPromptVersions = [];
    let currentPromptIndex = 0;
    let isExpanded = false;
    let isRawMetadataVisible = false;
    let isLocked = false;
    let isPromptEditing = false;

    // ==================== 初始化 ====================

    function init() {
        rightPanel = document.getElementById('rightPanel');
        toggleBtn = document.getElementById('toggleRightPanel');
        detailContent = document.getElementById('detailContent');
        detailPlaceholder = document.getElementById('detailPlaceholder');
        detailInfo = document.getElementById('detailInfo');
        detailImage = document.getElementById('detailImage');
        detailPreview = document.getElementById('detailPreview');
        fileInfo = document.getElementById('fileInfo');
        positivePrompt = document.getElementById('positivePrompt');
        negativePrompt = document.getElementById('negativePrompt');
        paramsGrid = document.getElementById('paramsGrid');
        rawMetadataPanel = document.getElementById('rawMetadataPanel');
        rawMetadataList = document.getElementById('rawMetadataList');
        imageTags = document.getElementById('imageTags');
        tagSelect = document.getElementById('tagSelect');
        btnToggleFavorite = document.getElementById('btnToggleFavorite');
        btnAddTag = document.getElementById('btnAddTag');
        btnPrevPrompt = document.getElementById('btnPrevPrompt');
        btnNextPrompt = document.getElementById('btnNextPrompt');
        btnDeletePrompt = document.getElementById('btnDeletePrompt');
        btnAddPrompt = document.getElementById('btnAddPrompt');
        promptVersionLabel = document.getElementById('promptVersionLabel');
        btnGeneratePrompt = document.getElementById('btnGeneratePrompt');
        btnStopGenerate = document.getElementById('btnStopGenerate');
        detailApiConfigSelect = document.getElementById('detailApiConfigSelect');
        btnToggleRawMetadata = document.getElementById('btnToggleRawMetadata');
        btnCopyRawMetadata = document.getElementById('btnCopyRawMetadata');
        btnCloseRawMetadata = document.getElementById('btnCloseRawMetadata');

        bindEvents();

        // ★ 恢复上次的面板展开/锁定状态（localStorage 同步读取，避免异步丢失）
        // 移动端默认展开并锁定
        const isMobile = _isMobile();
        const savedExpanded = isMobile ? 'true' : localStorage.getItem('panelExpanded');
        const savedLocked = isMobile ? 'true' : localStorage.getItem('panelLocked');
        if (savedLocked === 'true') {
            isLocked = true;
            const lockBtn = document.getElementById('lockRightPanel');
            if (lockBtn) {
                lockBtn.textContent = '';
                lockBtn.innerHTML = '<span class="icon icon-lock"></span>';
                lockBtn.title = _t("detail.lock_on");
                lockBtn.classList.add('locked');
            }
        }
        if (savedExpanded === 'false' || savedLocked === 'true') {
            isExpanded = false;
            if (isMobile) {
                // 移动端不需要 collapsed 类，由 CSS 控制
            } else {
                rightPanel.classList.add('collapsed');
                document.documentElement.style.setProperty('--right-panel-width', '0px');
            }
        } else {
            expand();
        }

        // 初始化 API 配置选择器
        refreshDetailApiConfigSelect();
    }

    function bindEvents() {
        // 面板切换
        if (toggleBtn) {
            toggleBtn.addEventListener('click', togglePanel);
        }

        // 在文件资源管理器中定位文件（通过服务端 API）
        document.getElementById('btnOpenOriginal').addEventListener('click', async () => {
            if (currentImage) {
                await ImageContextMenu.openFileLocation(currentImage.path);
            }
        });

        // 点击预览图打开大图查看器
        detailImage.addEventListener('click', () => {
            if (currentImage) {
                ImageViewer.open(currentImage);
            }
        });

        // 右键菜单 - 详情面板预览图
        detailImage.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            if (currentImage) {
                ImageContextMenu.show(e.clientX, e.clientY, currentImage.path, currentImage.rootPath, currentImage.folder);
            }
        });

        // 拖拽：DownloadURL 指定文件名 + 原图 URL
        detailImage.addEventListener('dragstart', (e) => {
            if (!currentImage) return;
            const ext = currentImage.name.split('.').pop().toLowerCase();
            const mimeMap = { jpg: 'image/jpeg', jpeg: 'image/jpeg', png: 'image/png', gif: 'image/gif', webp: 'image/webp', bmp: 'image/bmp', svg: 'image/svg+xml', mp4: 'video/mp4', webm: 'video/webm', mkv: 'video/x-matroska' };
            const mime = mimeMap[ext] || 'image/png';
            e.dataTransfer.setData('DownloadURL', mime + ':' + currentImage.name + ':' + currentImage.url);
            e.dataTransfer.setData('text/uri-list', currentImage.url);
            e.dataTransfer.setData('text/plain', currentImage.path);
            e.dataTransfer.effectAllowed = 'copy';
        });

        // 提示词版本导航
        btnPrevPrompt.addEventListener('click', () => navigatePrompt(-1));
        btnNextPrompt.addEventListener('click', () => navigatePrompt(1));
        btnDeletePrompt.addEventListener('click', deleteCurrentPrompt);
        btnAddPrompt.addEventListener('click', togglePromptEdit);

        // 标签操作
        btnAddTag.addEventListener('click', addTagToCurrentImage);
        btnToggleFavorite.addEventListener('click', toggleFavoriteCurrentImage);

        // AI 反推
        btnGeneratePrompt.addEventListener('click', generatePrompt);
        btnStopGenerate.addEventListener('click', stopGenerate);

        // API 配置选择器
        if (detailApiConfigSelect) {
            detailApiConfigSelect.addEventListener('change', async () => {
                const id = detailApiConfigSelect.value;
                if (id) {
                    if (typeof Storage !== 'undefined' && Storage.setSetting) {
                        await Storage.setSetting('activeApiConfigId', id);
                    }
                } else {
                    if (typeof Storage !== 'undefined' && Storage.setSetting) {
                        await Storage.setSetting('activeApiConfigId', null);
                    }
                }
            });
        }

        // 原始元数据
        btnToggleRawMetadata.addEventListener('click', toggleRawMetadata);
        btnCopyRawMetadata.addEventListener('click', copyRawMetadata);
        btnCloseRawMetadata.addEventListener('click', closeRawMetadata);

        // 常规浏览模式：P 键切换右侧面板
        document.addEventListener('keydown', (e) => {
            if ((e.key === 'p' || e.key === 'P') && !e.ctrlKey && !e.metaKey && !e.altKey) {
                const overlay = document.getElementById('imageViewerOverlay');
                if (overlay && overlay.style.display === 'flex') return; // 查看器打开时不抢事件
                if (document.activeElement && (document.activeElement.tagName === 'INPUT' || document.activeElement.tagName === 'TEXTAREA')) return;
                e.preventDefault();
                togglePanel();
            }
        });
    }

    // ==================== 面板切换 ====================

    function _isMobile() {
        return document.body.classList.contains('mobile');
    }

    function togglePanel() {
        if (_isMobile()) {
            // 移动端：使用抽屉式 mobile-open
            const overlay = document.getElementById('panelOverlay');
            const isOpen = rightPanel.classList.toggle('mobile-open');
            const btnInfo = document.getElementById('mobileNavInfo');
            if (overlay) overlay.classList.toggle('show', isOpen);
            if (btnInfo) btnInfo.classList.toggle('active', isOpen);
            isExpanded = isOpen;
            _savePanelState();
            return;
        }
        isExpanded = !isExpanded;
        rightPanel.classList.toggle('collapsed', !isExpanded);
        _savePanelState();
        document.documentElement.style.setProperty('--right-panel-width', isExpanded ? '420px' : '0px');
    }

    function expand() {
        if (isLocked) return;
        if (!isExpanded) {
            isExpanded = true;
            if (_isMobile()) {
                const overlay = document.getElementById('panelOverlay');
                rightPanel.classList.add('mobile-open');
                if (overlay) overlay.classList.add('show');
                const btnInfo = document.getElementById('mobileNavInfo');
                if (btnInfo) btnInfo.classList.add('active');
                _savePanelState();
                return;
            }
            rightPanel.classList.remove('collapsed');
            _savePanelState();
            document.documentElement.style.setProperty('--right-panel-width', '420px');
        }
    }

    function collapse() {
        if (isExpanded) {
            isExpanded = false;
            if (_isMobile()) {
                const overlay = document.getElementById('panelOverlay');
                rightPanel.classList.remove('mobile-open');
                if (overlay) overlay.classList.remove('show');
                const btnInfo = document.getElementById('mobileNavInfo');
                if (btnInfo) btnInfo.classList.remove('active');
                _savePanelState();
                return;
            }
            rightPanel.classList.add('collapsed');
            _savePanelState();
            document.documentElement.style.setProperty('--right-panel-width', '0px');
        }
    }

    function setLocked(locked) {
        isLocked = locked;
        const lockBtn = document.getElementById('lockRightPanel');
        if (lockBtn) {
            lockBtn.innerHTML = locked ? '<span class="icon icon-lock"></span>' : '<span class="icon icon-unlock"></span>';
            lockBtn.title = locked ? _t('detail.lock_on') : _t('detail.lock_off');
            lockBtn.classList.toggle('locked', locked);
        }
        if (locked) {
            collapse(); // 锁定时收回面板（collapse 内会调 _savePanelState）
        } else {
            expand();   // 解锁时常驻展开（expand 内会调 _savePanelState）
        }
    }

    function getLocked() {
        return isLocked;
    }

    function _savePanelState() {
        // localStorage 同步写入，确保关闭应用时不会丢失
        localStorage.setItem('panelExpanded', isExpanded);
        localStorage.setItem('panelLocked', isLocked);
        // 异步同步到服务器端 user-data.json
        if (typeof Storage !== 'undefined' && Storage.setSetting) {
            Storage.setSetting('panelExpanded', isExpanded);
            Storage.setSetting('panelLocked', isLocked);
        }
    }

    // ==================== 显示图片详情 ====================

    async function showImage(imgData) {
        const _t = (typeof I18n !== "undefined" ? I18n.t : (s) => s);
        // 锁定模式下不加载详情，由 Gallery 的单击放大替代
        if (isLocked) return;
        if (isPromptEditing) cancelPromptEdit();
        currentImage = imgData;
        expand();
        refreshDetailApiConfigSelect();

        console.log('[Detail] showImage:', imgData.name,
            'hasMetadata:', !!imgData.metadata,
            'url:', imgData.url,
            '_fromServer:', imgData._fromServer,
            'hasFile:', !!imgData.file,
            'Gallery available:', typeof Gallery !== 'undefined',
            'resolveMetadataOnDemand available:', typeof Gallery !== 'undefined' && !!Gallery.resolveMetadataOnDemand);

        detailPlaceholder.style.display = 'none';
        detailInfo.style.display = 'block';

        // 大图预览：视频使用 <video>，图片使用 <img>
        const existingVideo = detailPreview.querySelector('video');
        if (existingVideo) existingVideo.remove();

        if (imgData.isVideo) {
            detailImage.style.display = 'none';
            const videoEl = document.createElement('video');
            videoEl.src = imgData.url;
            videoEl.controls = true;
            videoEl.style.width = '100%';
            videoEl.style.maxHeight = '400px';
            videoEl.style.background = '#000';
            videoEl.style.borderRadius = 'var(--radius)';
            videoEl.preload = 'metadata';
            videoEl.onerror = function() {
                console.warn('[Detail] 视频无法播放:', imgData.name);
                App.showToast(_t('detail.video_unsupported'), 'warning');
                videoEl.remove();
                detailImage.style.display = '';
            };
            detailPreview.appendChild(videoEl);
        } else {
            detailImage.style.display = '';
            detailImage.src = imgData.url;
            detailImage.alt = imgData.name;
        }

        // 文件信息
        renderFileInfo(imgData);

        // 按需解析元数据（仅在首次点击时解析，避免批量导入时全部解析）
        // ★ Gallery.resolveMetadataOnDemand 已支持 Wails 环境（通过 HTTP 获取图片数据）
        if (!imgData.isVideo && !imgData.metadata && typeof Gallery !== 'undefined' && Gallery.resolveMetadataOnDemand) {
            console.log('[Detail] 调用 resolveMetadataOnDemand...');
            try {
                const meta = await Gallery.resolveMetadataOnDemand(imgData);
                console.log('[Detail] resolveMetadataOnDemand 返回:',
                    'prompt:', meta?.prompt ? meta.prompt.substring(0, 50) + '...' : '(空)',
                    'params:', Object.keys(meta?.params || {}).length,
                    'raw:', Object.keys(meta?.raw || {}).length);
            } catch (err) {
                console.warn('[Detail] 按需解析元数据失败:', err);
            }
        } else {
            console.log('[Detail] 跳过元数据解析:',
                'hasMetadata:', !!imgData.metadata,
                'Gallery:', typeof Gallery !== 'undefined',
                'resolveFn:', typeof Gallery !== 'undefined' && !!Gallery.resolveMetadataOnDemand);
        }

        // 加载提示词版本
        try {
            await loadPromptVersions(imgData);
        } catch (err) {
            console.warn('[Detail] 加载提示词版本失败（非关键错误）:', err.message);
        }

        // 加载参数
        console.log('[Detail] 渲染参数, metadata:', !!imgData.metadata, 'params:', imgData.metadata ? Object.keys(imgData.metadata.params || {}) : 'N/A');
        renderParams(imgData);
        renderRawMetadata(imgData);

        // 加载标签和收藏
        try {
            await loadTagsAndFavorites(imgData);
        } catch (err) {
            console.warn('[Detail] 加载标签和收藏失败（非关键错误）:', err.message);
        }
    }

    function hideImage() {
        currentImage = null;
        detailPlaceholder.style.display = 'block';
        detailInfo.style.display = 'none';
        detailImage.src = '';
        isRawMetadataVisible = false;
        if (rawMetadataPanel) rawMetadataPanel.classList.add('hidden');
        if (btnToggleRawMetadata) btnToggleRawMetadata.textContent = _t("detail.metadata");
    }

    // ==================== 文件信息 ====================

    function renderFileInfo(imgData) {
        // 文件大小：智能转换为 KB / MB
        let sizeDisplay = '?';
        if (imgData.size) {
            const sizeKB = imgData.size / 1024;
            if (sizeKB < 1) {
                sizeDisplay = `${imgData.size} B`;
            } else if (sizeKB < 1024) {
                sizeDisplay = `${sizeKB.toFixed(1)} KB`;
            } else {
                sizeDisplay = `${(imgData.size / (1024 * 1024)).toFixed(1)} MB`;
            }
        }
        const createdAt = imgData.createdAt && imgData.createdAt > 0
            ? new Date(imgData.createdAt).toLocaleString('zh-CN')
            : _t('detail.unknown');
        const modifiedAt = imgData.lastModified
            ? new Date(imgData.lastModified).toLocaleString('zh-CN')
            : _t('detail.unknown');

        const ext = imgData.name.split('.').pop().toUpperCase();

        fileInfo.innerHTML = `
            <div class="info-item"><span class="info-key">${_t('detail.file_name')}</span><br><span class="info-val">${escapeHtml(imgData.name)}</span></div>
            <div class="info-item"><span class="info-key">${_t('detail.dimensions')}</span><br><span class="info-val">${imgData.width && imgData.height ? imgData.width + ' x ' + imgData.height : _t('detail.unknown')}</span></div>
            <div class="info-item"><span class="info-key">${_t('detail.format')}</span><br><span class="info-val">${ext}</span></div>
            <div class="info-item"><span class="info-key">${_t('detail.file_size')}</span><br><span class="info-val">${sizeDisplay}</span></div>
            <div class="info-item"><span class="info-key">${_t('detail.created_time')}</span><br><span class="info-val">${createdAt}</span></div>
            <div class="info-item"><span class="info-key">${_t('detail.modified_date')}</span><br><span class="info-val">${modifiedAt}</span></div>
        `;
    }

    // ==================== 提示词版本管理 ====================

    async function loadPromptVersions(imgData) {
        currentPromptVersions = await Storage.getPromptVersions(imgData.path);

        // 如果没有存储的版本，从元数据创建原始版本
        if (currentPromptVersions.length === 0 && imgData.metadata) {
            const meta = imgData.metadata;
            if (meta.prompt || meta.negativePrompt) {
                const originalVersion = await Storage.addPromptVersion(imgData.path, {
                    positivePrompt: meta.prompt,
                    negativePrompt: meta.negativePrompt,
                    source: 'original'
                });
                currentPromptVersions = [originalVersion];
                if (typeof Gallery !== 'undefined') { Gallery.refreshPromptCounts(); }
            }
        }

        currentPromptIndex = currentPromptVersions.length > 0 ? 0 : -1;
        renderCurrentPrompt();
    }

    function renderCurrentPrompt() {
        if (isPromptEditing) return;

        // 如果 DOM 被编辑模式替换了，先恢复原始 <pre> 结构
        const posEl = document.getElementById('positivePrompt');
        const negEl = document.getElementById('negativePrompt');
        if (!posEl || posEl.tagName !== 'PRE') {
            document.getElementById('promptDisplay').innerHTML = `
                <div class="prompt-block"><label>Positive Prompt</label><pre id="positivePrompt"></pre></div>
                <div class="prompt-block"><label>Negative Prompt</label><pre id="negativePrompt"></pre></div>`;
            // 重新缓存引用
            positivePrompt = document.getElementById('positivePrompt');
            negativePrompt = document.getElementById('negativePrompt');
        }

        if (currentPromptIndex >= 0 && currentPromptIndex < currentPromptVersions.length) {
            const version = currentPromptVersions[currentPromptIndex];
            positivePrompt.textContent = version.positivePrompt || _t('detail.none');
            negativePrompt.textContent = version.negativePrompt || _t('detail.none');
            promptVersionLabel.textContent = `${currentPromptIndex + 1}/${currentPromptVersions.length}`;

            const sourceLabel = version.source === 'original' ? _t('detail.prompt_original') :
                               version.source === 'ai_generated' ? _t('detail.prompt_ai') : _t('detail.prompt_custom');
            promptVersionLabel.title = `来源: ${sourceLabel}`;
        } else {
            positivePrompt.textContent = _t("detail.no_prompt");
            negativePrompt.textContent = _t("detail.none");
            promptVersionLabel.textContent = '0/0';
        }

        // 恢复添加按钮状态
        btnAddPrompt.innerHTML = '<span class="icon icon-add"></span>';
        btnAddPrompt.title = _t('detail.add_prompt');
        btnAddPrompt.classList.remove('save-mode');

        // 更新按钮状态
        btnPrevPrompt.disabled = currentPromptIndex <= 0;
        btnNextPrompt.disabled = currentPromptIndex >= currentPromptVersions.length - 1;
        btnDeletePrompt.style.display = currentPromptVersions.length > 0 ? 'inline-block' : 'none';

        // 为提示词块添加展开/收起按钮
        addPromptExpandButtons();
        // 为提示词块添加复制按钮
        addPromptCopyButtons();
    }

    function addPromptCopyButtons() {
        const promptBlocks = document.querySelectorAll('.prompt-block');
        promptBlocks.forEach(block => {
            // 移除旧按钮
            const oldBtn = block.querySelector('.prompt-copy-btn');
            if (oldBtn) oldBtn.remove();

            const pre = block.querySelector('pre');
            if (!pre) return;

            const btn = document.createElement('button');
            btn.className = 'prompt-copy-btn';
            btn.textContent = ''; btn.innerHTML = '<span class="icon icon-copy"></span> ' + _t('detail.copy');
            btn.title = _t('detail.copy_prompt');

            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                const text = pre.textContent || '';
                copyToClipboard(text, block.querySelector('label')?.textContent || _t('detail.prompt'));
            });

            block.appendChild(btn);
        });
    }

    function addPromptExpandButtons() {
        const promptBlocks = document.querySelectorAll('.prompt-block');
        promptBlocks.forEach(block => {
            // 移除旧按钮
            const oldBtn = block.querySelector('.prompt-expand-btn');
            if (oldBtn) oldBtn.remove();

            const pre = block.querySelector('pre');
            if (!pre) return;

            // 检查内容是否超出 max-height（300px）
            const needsExpand = pre.scrollHeight > pre.clientHeight + 5;

            const btn = document.createElement('button');
            btn.className = 'prompt-expand-btn';
            btn.textContent = _t('detail.expand');
            btn.title = _t('detail.click_to_toggle');

            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                const isExpanded = pre.classList.toggle('expanded');
                btn.textContent = isExpanded ? _t('detail.collapse') : _t('detail.expand');
            });

            block.appendChild(btn);

            // 如果内容不够长，隐藏按钮
            if (!needsExpand) {
                btn.style.display = 'none';
            }
        });
    }

    function navigatePrompt(direction) {
        const newIndex = currentPromptIndex + direction;
        if (newIndex >= 0 && newIndex < currentPromptVersions.length) {
            currentPromptIndex = newIndex;
            renderCurrentPrompt();
        }
    }

    async function deleteCurrentPrompt() {
        if (currentPromptVersions.length === 0) return;
        if (!confirm(_t('detail.confirm_delete_prompt'))) return;

        const version = currentPromptVersions[currentPromptIndex];
        await Storage.deletePromptVersion(version.id);
        currentPromptVersions.splice(currentPromptIndex, 1);
        if (typeof Gallery !== 'undefined') { Gallery.refreshPromptCounts(); }

        if (currentPromptVersions.length === 0) {
            currentPromptIndex = -1;
        } else if (currentPromptIndex >= currentPromptVersions.length) {
            currentPromptIndex = currentPromptVersions.length - 1;
        }
        renderCurrentPrompt();
        App.showToast(_t('detail.prompt_deleted'), 'success');
    }

    function enterPromptEdit() {
        isPromptEditing = true;
        const posText = positivePrompt.textContent === _t('detail.none') || positivePrompt.textContent === _t('detail.no_prompt') ? '' : positivePrompt.textContent;
        const negText = negativePrompt.textContent === _t('detail.none') || negativePrompt.textContent === _t('detail.no_prompt') ? '' : negativePrompt.textContent;

        positivePrompt.outerHTML = `<div class="prompt-textarea-wrapper"><textarea id="positivePrompt" class="prompt-textarea">${escapeHtml(posText)}</textarea><div class="prompt-resize-handle"></div></div>`;
        negativePrompt.outerHTML = `<div class="prompt-textarea-wrapper"><textarea id="negativePrompt" class="prompt-textarea">${escapeHtml(negText)}</textarea><div class="prompt-resize-handle"></div></div>`;

        // 右下角拖拽缩放（两个 textarea 统一样式）
        document.querySelectorAll('.prompt-resize-handle').forEach(handle => {
            const taWrapper = handle.parentElement;
            const ta = taWrapper.querySelector('textarea');
            if (!ta) return;
            handle.addEventListener('mousedown', (e) => {
                e.preventDefault();
                const startY = e.clientY;
                const startH = ta.offsetHeight;
                const onMove = (ev) => {
                    ta.style.height = Math.max(60, startH + (ev.clientY - startY)) + 'px';
                };
                const onUp = () => {
                    document.removeEventListener('mousemove', onMove);
                    document.removeEventListener('mouseup', onUp);
                };
                document.addEventListener('mousemove', onMove);
                document.addEventListener('mouseup', onUp);
            });
        });

        // 按钮切换
        btnAddPrompt.innerHTML = '<span class="icon icon-save"></span>';
        btnAddPrompt.title = _t('detail.save_prompt');
        btnAddPrompt.classList.add('save-mode');
        btnDeletePrompt.style.display = 'none';
    }

    async function savePromptEdit() {
        const posTextarea = document.getElementById('positivePrompt');
        const negTextarea = document.getElementById('negativePrompt');
        const posPrompt = (posTextarea?.value || '').trim();
        const negPrompt = (negTextarea?.value || '').trim();

        if (!posPrompt && !negPrompt) {
            cancelPromptEdit();
            return;
        }

        const version = await Storage.addPromptVersion(currentImage.path, {
            positivePrompt: posPrompt,
            negativePrompt: negPrompt,
            source: 'custom'
        });

        currentPromptVersions.push(version);
        if (typeof Gallery !== 'undefined') { Gallery.refreshPromptCounts(); }
        currentPromptIndex = currentPromptVersions.length - 1;
        isPromptEditing = false;
        renderCurrentPrompt();
        App.showToast(_t('detail.prompt_saved'), 'success');
    }

    function cancelPromptEdit() {
        isPromptEditing = false;
        renderCurrentPrompt();
    }

    async function togglePromptEdit() {
        if (isPromptEditing) {
            await savePromptEdit();
        } else {
            enterPromptEdit();
        }
    }

    // ==================== AI 反推提示词 ====================

    async function generatePrompt() {
        if (!currentImage) return;

        // 获取 API 配置：优先使用右侧面板选择的配置，否则使用默认配置
        const selectedId = detailApiConfigSelect?.value;
        let config = null;
        if (selectedId && typeof Storage !== 'undefined' && Storage.getAllApiConfigs) {
            const configs = await Storage.getAllApiConfigs();
            config = configs.find(c => c.id === selectedId) || null;
        }
        if (!config) {
            config = typeof Storage !== 'undefined' ? await Storage.getDefaultApiConfig() : null;
        }
        if (!config) {
            App.showToast(_t('detail.api_not_configured'), 'warning');
            Sidebar.switchTab('api');
            return;
        }

        // 切换按钮：隐藏"AI 反推"，显示"停止"
        btnGeneratePrompt.style.display = 'none';
        btnStopGenerate.style.display = '';
        generateAbortController = new AbortController();

        try {
            // 获取图片 Base64
            let base64;

            // 方法1：Wails 环境 — 通过 Go 桥接直接获取图片（绕过 HTTP/CORS）
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails() && currentImage.id) {
                try {
                    const fileData = await WailsBridge.getImageFile(currentImage.id);
                    if (fileData && fileData.data) {
                        base64 = fileData.data;
                    }
                } catch (e) {
                    console.warn('[Detail] WailsBridge.getImageFile 失败，回退到其他方式:', e.message);
                }
            }

            // 方法2：File 对象（浏览器 File System Access API）
            if (!base64 && currentImage.file) {
                base64 = await ApiService.fileToBase64(currentImage.file);
            }

            // 方法3：通过 URL 加载（blob: / data: / http:）
            if (!base64 && currentImage.thumbnailUrl) {
                base64 = await ApiService.urlToBase64(currentImage.thumbnailUrl);
            }

            // 方法4：通过图片原始 URL 加载
            if (!base64 && currentImage.url) {
                base64 = await ApiService.urlToBase64(currentImage.url);
            }

            if (!base64) {
                throw new Error(_t('detail.cannot_get_image_data'));
            }

            const result = await ApiService.reversePrompt(base64, config, generateAbortController.signal);

            // 保存为新的提示词版本
            const version = await Storage.addPromptVersion(currentImage.path, {
                positivePrompt: result.positivePrompt,
                negativePrompt: result.negativePrompt,
                source: 'ai_generated'
            });

            currentPromptVersions.push(version);
            currentPromptIndex = currentPromptVersions.length - 1;
            renderCurrentPrompt();
            if (typeof Gallery !== 'undefined') { Gallery.refreshPromptCounts(); }
            App.showToast(_t('detail.ai_reverse_done'), 'success');
        } catch (err) {
            console.error('[Detail] AI 反推失败:', err);
            if (err.message === '已取消') {
                App.showToast(_t('detail.generation_stopped'), 'info');
                return;
            }
            // 提供更详细的错误提示
            let errorMsg = err.message;
            if (errorMsg.includes('Failed to fetch') || errorMsg.includes('TypeError')) {
                errorMsg = _t('detail.network_error');
            }
            App.showToast(_t('detail.ai_reverse_failed') + ': ' + errorMsg, 'error');
        } finally {
            btnGeneratePrompt.style.display = '';
            btnStopGenerate.style.display = 'none';
            generateAbortController = null;
        }
    }

    function stopGenerate() {
        if (generateAbortController) {
            generateAbortController.abort();
        }
        // 同时取消 Go 后端的代理请求（Wails 环境）
        if (typeof ApiService !== 'undefined' && ApiService.cancelCurrentRequest) {
            ApiService.cancelCurrentRequest();
        }
    }

    async function refreshDetailApiConfigSelect() {
        if (!detailApiConfigSelect) return;
        try {
            const configs = typeof Storage !== 'undefined' && Storage.getAllApiConfigs
                ? await Storage.getAllApiConfigs()
                : [];
            const activeId = typeof Storage !== 'undefined' && Storage.getSetting
                ? await Storage.getSetting('activeApiConfigId', null)
                : null;

            detailApiConfigSelect.innerHTML = '<option value="">' + _t('detail.select_api_config') + '</option>';
            for (const c of configs) {
                const option = document.createElement('option');
                option.value = c.id;
                option.textContent = c.name + (c.isDefault ? _t('detail.default_label') : '');
                if (c.id === activeId) option.selected = true;
                detailApiConfigSelect.appendChild(option);
            }
        } catch (e) {
            console.warn('[Detail] 刷新 API 配置选择器失败:', e.message);
        }
    }

    // ==================== 参数展示 ====================

    // 重要参数白名单（只展示这些关键生成参数）
    const IMPORTANT_PARAMS = new Set([
        'Steps', 'Sampler', 'Scheduler', 'Schedule type',
        'CFG Scale', 'Distilled CFG Scale', 'Flux Guidance',
        'Seed', 'Size', 'Width', 'Height',
        'Model', 'Model hash', 'VAE', 'CLIP', 'Clip Skip',
        'LoRA',
        'Upscaler', 'Refiner', 'Hires Fix',
        'Denoise', 'ControlNet',
        'Batch Size', 'ENSD', 'Token Merging'
    ]);

    // 参数显示顺序（按此顺序排列）
    const PARAM_ORDER = [
        'Model', 'Model hash', 'VAE', 'CLIP', 'Clip Skip',
        'LoRA',
        'Size', 'Width', 'Height',
        'Steps', 'Sampler', 'Scheduler', 'Schedule type',
        'CFG Scale', 'Distilled CFG Scale', 'Flux Guidance',
        'Seed',
        'Denoise', 'Upscaler', 'Refiner', 'Hires Fix',
        'ControlNet',
        'Batch Size', 'ENSD', 'Token Merging'
    ];

    function renderPhotoInfoPanel(imgData, raw) {
        // 文件大小格式化
        let sizeStr = '?';
        if (imgData.size) {
            const kb = imgData.size / 1024;
            sizeStr = kb < 1 ? `${imgData.size} B` : kb < 1024 ? `${kb.toFixed(1)} KB` : `${(imgData.size / (1024 * 1024)).toFixed(2)} MB`;
        }
        // 修改时间格式化
        const modTime = imgData.lastModified ? new Date(imgData.lastModified).toLocaleString('zh-CN') : '?';
        // 图片尺寸
        const dims = (imgData.width && imgData.height) ? `${imgData.width} x ${imgData.height}` : '?';
        // 文件类型
        const ext = imgData.name ? imgData.name.split('.').pop().toUpperCase() : '?';

        // 基本信息行: [label, value]
        const basicInfo = [
            [_t('detail.photo_name'), imgData.name || '?'],
            [_t('detail.photo_type'), ext],
            [_t('detail.photo_size'), sizeStr],
            [_t('detail.photo_dimensions'), dims],
            [_t('detail.photo_mod_time'), modTime],
        ];

        // EXIF 字段（按参考面板顺序，只保留 raw 中存在的）
        const exifOrder = [
            _t('detail.photo_capture_time'), _t('detail.photo_camera_make'), _t('detail.photo_camera_model'), _t('detail.photo_lens'),
            _t('detail.photo_aperture'), _t('detail.photo_max_aperture'), _t('detail.photo_exposure'), _t('detail.photo_exposure_comp'),
            _t('detail.photo_iso'), _t('detail.photo_focal'), _t('detail.photo_metering'), _t('detail.photo_flash'), _t('detail.photo_white_balance'), _t('detail.photo_exposure_program'),
        ];

        const exifRows = [];
        for (const key of exifOrder) {
            if (raw[key] !== undefined && raw[key] !== '') {
                exifRows.push([key, raw[key]]);
            }
        }

        // 构建 HTML
        let html = '<div class="photo-info-panel">';
        html += '<div class="photo-info-header">' + _t('detail.photo_info') + '</div>';

        html += '<div class="photo-info-section">';
        for (const [label, value] of basicInfo) {
            html += `<div class="photo-info-row"><span class="photo-info-label">${label}</span><span class="photo-info-value">${escapeHtml(String(value))}</span></div>`;
        }
        html += '</div>';

        if (exifRows.length > 0) {
            html += '<div class="photo-info-section">';
            for (const [label, value] of exifRows) {
                html += `<div class="photo-info-row"><span class="photo-info-label">${label}</span><span class="photo-info-value">${escapeHtml(String(value))}</span></div>`;
            }
            html += '</div>';
        }

        html += '</div>';
        paramsGrid.innerHTML = html;
    }

    function renderParams(imgData) {
        paramsGrid.innerHTML = '';

        // 检测是否有相机 EXIF 数据
        const raw = imgData.metadata && imgData.metadata.raw ? imgData.metadata.raw : null;
        const exifKeys = [_t('detail.photo_camera_make'), _t('detail.photo_camera_model'), _t('detail.photo_capture_time'), _t('detail.photo_exposure'), _t('detail.photo_aperture'), _t('detail.photo_iso'), _t('detail.photo_focal'), _t('detail.photo_lens')];
        const hasExif = raw && exifKeys.some(k => raw[k] !== undefined);

        if (hasExif) {
            renderPhotoInfoPanel(imgData, raw);
            return;
        }

        if (!imgData.metadata || !imgData.metadata.params || Object.keys(imgData.metadata.params).length === 0) {
            paramsGrid.innerHTML = '<p class="text-muted" style="font-size:12px;">' + _t('detail.no_params') + '</p>';
            return;
        }

        const params = imgData.metadata.params;

        // 1. 收集 LoRA 信息（支持多个 LoRA）
        // 数据来源：
        //   a) params['LoRA'] - 从参数行解析（可能包含名称、哈希或两者混合，用 | 分隔）
        //   b) imgData.metadata.prompt - prompt 文本中的 <lora:name:weight> 标签
        const loraNames = [];
        const loraHashes = {};

        // 辅助函数：添加 LoRA 名称（去重）
        function addLoraName(name, weight) {
            const display = weight ? `${name} (${weight})` : name;
            if (name && !loraNames.includes(display)) {
                loraNames.push(display);
            }
        }

        // 辅助函数：从文本中提取 <lora:name:weight> 标签
        function extractLoraTags(text) {
            if (!text) return;
            const regex = /<lora:([^>]+)>/gi;
            let match;
            while ((match = regex.exec(text)) !== null) {
                const inner = match[1].trim();
                const parts = inner.split(':');
                const name = parts[0].trim();
                const weight = parts.length > 1 ? parts.slice(1).join(':') : '';
                addLoraName(name, weight || '1');
            }
        }

        // 从 prompt 中提取 <lora:...> 标签
        if (imgData.metadata && imgData.metadata.prompt) {
            extractLoraTags(imgData.metadata.prompt);
        }

        // 从 params['LoRA'] 解析
        if (params['LoRA']) {
            const loraVal = String(params['LoRA']);

            // 先按 | 分割（metadata.js 合并多个 LoRA 值时使用 | 分隔符）
            let parts = loraVal.split('|').map(s => s.trim()).filter(Boolean);

            // 如果 | 分割后只有一段，尝试按逗号分割（兼容原始逗号分隔格式）
            if (parts.length <= 1 && loraVal.includes(',')) {
                parts = loraVal.split(',').map(s => s.trim()).filter(Boolean);
            }

            for (const part of parts) {
                // 尝试解析哈希映射格式: "name: hash" 或 '"name": "hash"'
                const hashMatch = part.match(/^["']?([a-zA-Z0-9_\-\.\s\u4e00-\u9fff]+)["']?\s*:\s*["']?([a-f0-9]+)["']?$/i);
                if (hashMatch) {
                    const loraName = hashMatch[1].trim();
                    const loraHash = hashMatch[2];
                    loraHashes[loraName] = loraHash;
                    addLoraName(loraName);
                    continue;
                }

                // 尝试解析 <lora:name:weight> 格式
                const angleMatch = part.match(/<lora:([^>]+)>/i);
                if (angleMatch) {
                    const inner = angleMatch[1].trim();
                    const innerParts = inner.split(':');
                    const name = innerParts[0].trim();
                    const weight = innerParts.length > 1 ? innerParts.slice(1).join(':') : '';
                    addLoraName(name, weight);
                    continue;
                }

                // 尝试解析 "name:weight" 或 "name" 格式
                const colonMatch = part.match(/^([a-zA-Z0-9_\-\.\s\u4e00-\u9fff]+?)\s*:\s*([\d.]+)$/);
                if (colonMatch) {
                    addLoraName(colonMatch[1].trim(), colonMatch[2]);
                    continue;
                }

                // 纯名称
                if (part && !/^[a-f0-9]{6,}$/i.test(part)) {
                    addLoraName(part);
                }
            }
        }

        // 2. 过滤并排序参数
        const filteredEntries = [];

        for (const [key, value] of Object.entries(params)) {
            // 跳过 LoRA（已在上面单独处理）
            if (key === 'LoRA') continue;

            // 只展示白名单中的参数
            if (!IMPORTANT_PARAMS.has(key)) continue;

            const strValue = String(value);
            if (!strValue || strValue === 'undefined' || strValue === 'null') continue;

            const orderIndex = PARAM_ORDER.indexOf(key);
            filteredEntries.push({
                key,
                value: strValue,
                order: orderIndex >= 0 ? orderIndex : 999
            });
        }

        // 3. 如果有 LoRA，添加到过滤结果中
        if (loraNames.length > 0) {
            const loraDisplay = loraNames.map(name => {
                // 检查是否有对应的哈希
                const baseName = name.replace(/\s*\([\d.]+\)$/, '').trim();
                const hash = loraHashes[baseName] || '';
                if (hash) {
                    return `${name} [${hash.substring(0, 8)}]`;
                }
                return name;
            }).join('\n');

            filteredEntries.push({
                key: 'LoRA',
                value: loraDisplay,
                order: PARAM_ORDER.indexOf('LoRA')
            });
        }

        // 4. 按顺序排序
        filteredEntries.sort((a, b) => a.order - b.order);

        // 5. 渲染
        for (const { key, value } of filteredEntries) {
            const item = document.createElement('div');
            item.className = 'param-item';

            const keySpan = document.createElement('span');
            keySpan.className = 'param-key';
            keySpan.textContent = key;

            const valSpan = document.createElement('span');
            valSpan.className = 'param-val';

            // 格式化显示值
            let displayValue = value;

            // 如果值是 JSON 数组格式（如 "[210, 344.9]"），尝试解析为可读格式
            if (/^\[[\d\.,\seE+\-]+\]$/.test(value.trim())) {
                try {
                    const parsed = JSON.parse(value);
                    if (Array.isArray(parsed) && parsed.length >= 1) {
                        // 取第一个数值，尝试作为文件大小（bytes）转换为 MB
                        const firstNum = typeof parsed[0] === 'number' ? parsed[0] : parseFloat(parsed[0]);
                        if (!isNaN(firstNum) && firstNum > 0) {
                            const sizeMB = firstNum / (1024 * 1024);
                            displayValue = sizeMB.toFixed(1) + ' MB';
                        }
                    }
                } catch (e) {
                    // 解析失败，使用原值
                }
            }

            // 如果值包含换行，使用 pre-line 保留换行
            if (displayValue.includes('\n')) {
                valSpan.style.whiteSpace = 'pre-line';
            }
            valSpan.textContent = displayValue;

            item.appendChild(keySpan);
            item.appendChild(valSpan);

            // 添加复制按钮（hover 显示）
            const copyBtn = document.createElement('button');
            copyBtn.className = 'param-copy-btn';
            copyBtn.innerHTML = '<span class="icon icon-copy"></span>';
            copyBtn.title = `复制 ${key}`;
            copyBtn.addEventListener('click', (e) => {
                e.stopPropagation();
                copyToClipboard(value, key);
            });
            item.appendChild(copyBtn);

            paramsGrid.appendChild(item);
        }

        // 如果没有显示任何参数
        if (filteredEntries.length === 0) {
            paramsGrid.innerHTML = '<p class="text-muted" style="font-size:12px;">' + _t('detail.no_key_params') + '</p>';
        }
    }

    function toggleRawMetadata() {
        if (!rawMetadataPanel) return;
        isRawMetadataVisible = !isRawMetadataVisible;
        rawMetadataPanel.classList.toggle('hidden', !isRawMetadataVisible);
        btnToggleRawMetadata.textContent = isRawMetadataVisible ? _t('detail.close_metadata') : _t('detail.metadata');
    }

    function closeRawMetadata() {
        if (!rawMetadataPanel) return;
        isRawMetadataVisible = false;
        rawMetadataPanel.classList.add('hidden');
        if (btnToggleRawMetadata) btnToggleRawMetadata.textContent = _t('detail.metadata');
    }

    function copyRawMetadata() {
        if (!currentImage || !currentImage.metadata || !currentImage.metadata.raw) {
            App.showToast(_t('detail.no_metadata_copy'), 'warning');
            return;
        }
        const formatted = formatRawMetadataForCopy(currentImage.metadata.raw);
        copyToClipboard(formatted, _t('detail.metadata'));
    }

    function renderRawMetadata(imgData) {
        if (!rawMetadataList || !rawMetadataPanel) return;

        rawMetadataList.innerHTML = '';

        const raw = imgData.metadata && imgData.metadata.raw ? imgData.metadata.raw : null;
        if (!raw || Object.keys(raw).length === 0) {
            isRawMetadataVisible = false;
            rawMetadataPanel.classList.add('hidden');
            if (btnToggleRawMetadata) btnToggleRawMetadata.textContent = _t('detail.metadata');
            rawMetadataList.innerHTML = '<p class="text-muted" style="font-size:12px;">' + _t('detail.no_metadata') + '</p>';
            return;
        }

        // 检测是否包含相机 EXIF 数据，有则自动展开
        const cameraExifKeys = [_t('detail.photo_camera_make'), _t('detail.photo_camera_model'), _t('detail.photo_capture_time'), _t('detail.photo_exposure'), _t('detail.photo_aperture'), _t('detail.photo_iso'), _t('detail.photo_focal'), _t('detail.photo_lens'), _t('detail.photo_flash'), _t('detail.photo_metering'), _t('detail.photo_exposure_program'), _t('detail.photo_white_balance'), _t('detail.photo_max_aperture'), _t('detail.photo_exposure_comp'), 'GPS纬度', 'GPS经度'];
        const hasCameraExif = cameraExifKeys.some(k => raw[k] !== undefined);


        const entries = Object.entries(raw).sort((a, b) => a[0].localeCompare(b[0], 'zh-CN'));

        for (const [key, value] of entries) {
            const item = document.createElement('div');
            item.className = 'raw-meta-item';

            const header = document.createElement('div');
            header.className = 'raw-meta-header';

            const keyEl = document.createElement('span');
            keyEl.className = 'raw-meta-key';
            keyEl.textContent = key;

            const metaType = document.createElement('span');
            metaType.className = 'raw-meta-type';
            metaType.textContent = guessRawValueType(value);

            // 展开/折叠箭头
            const toggleArrow = document.createElement('span');
            toggleArrow.className = 'raw-meta-toggle';
            toggleArrow.textContent = '▶';
            toggleArrow.style.cssText = 'font-size:10px;margin-right:6px;transition:transform 0.2s;display:inline-block;';

            header.appendChild(toggleArrow);
            header.appendChild(keyEl);
            header.appendChild(metaType);

            const body = document.createElement('pre');
            body.className = 'raw-meta-value';
            body.textContent = formatRawValue(value);
            // 相机 EXIF 字段默认展开，其他默认折叠
            const isExifField = cameraExifKeys.includes(key);
            body.style.display = isExifField ? 'block' : 'none';
            toggleArrow.textContent = isExifField ? '▼' : '▶';

            // 点击 header 展开/折叠 body
            header.addEventListener('click', (e) => {
                e.stopPropagation();
                const isHidden = body.style.display === 'none';
                body.style.display = isHidden ? 'block' : 'none';
                toggleArrow.textContent = isHidden ? '▼' : '▶';
            });

            // 添加复制按钮到 body
            const copyBtn = document.createElement('button');
            copyBtn.className = 'raw-meta-copy-btn';
            copyBtn.innerHTML = '<span class="icon icon-copy"></span> ' + _t('detail.copy');
            copyBtn.title = _t('detail.copy') + ' ' + key;
            copyBtn.addEventListener('click', (e) => {
                e.stopPropagation();
                copyToClipboard(`${key}\n${formatRawValue(value)}`, _t('detail.metadata') + ' ' + key);
            });
            body.appendChild(copyBtn);

            item.appendChild(header);
            item.appendChild(body);

            rawMetadataList.appendChild(item);
        }
    }

    function formatRawValue(value) {
        if (value === null || value === undefined) return '';
        if (typeof value === 'string') {
            const trimmed = value.trim();
            if (!trimmed) return '';
            try {
                const parsed = JSON.parse(trimmed);
                return JSON.stringify(parsed, null, 2);
            } catch (e) {
                return trimmed;
            }
        }
        try {
            return JSON.stringify(value, null, 2);
        } catch (e) {
            return String(value);
        }
    }

    function formatRawMetadataForCopy(raw) {
        const lines = [];
        const entries = Object.entries(raw).sort((a, b) => a[0].localeCompare(b[0], 'zh-CN'));
        for (const [key, value] of entries) {
            lines.push(`### ${key}`);
            lines.push(formatRawValue(value));
            lines.push('');
        }
        return lines.join('\n').trim();
    }

    function guessRawValueType(value) {
        if (value === null || value === undefined) return 'empty';
        if (typeof value !== 'string') return typeof value;
        const trimmed = value.trim();
        if (!trimmed) return 'text';
        if ((trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']'))) {
            try {
                JSON.parse(trimmed);
                return 'json';
            } catch (e) {
                return 'text';
            }
        }
        return 'text';
    }

    // ==================== 标签与收藏 ====================

    async function loadTagsAndFavorites(imgData) {
        // 标签
        const tags = await Storage.getTagsForImage(imgData.path);
        renderImageTags(tags);

        // 收藏状态
        const isFav = await Storage.isFavorite(imgData.path);
        btnToggleFavorite.innerHTML = isFav ? '<span class="icon icon-favorite-on"></span> ' + _t('detail.unfavorite') : '<span class="icon icon-favorite-off"></span> ' + _t('detail.favorite');
        btnToggleFavorite.classList.toggle('btn-danger', isFav);

        // 更新标签选择器
        Sidebar.updateTagSelect();

        // 同步刷新画廊（重新读取数据以更新筛选视图）
        if (typeof Gallery !== 'undefined' && Gallery.refreshCurrentFilteredView) {
            Gallery.refreshCurrentFilteredView();
        }
    }

    function renderImageTags(tags) {
        imageTags.innerHTML = '';
        if (tags.length === 0) {
            imageTags.innerHTML = '<span class="text-muted" style="font-size:12px;">' + _t('detail.no_tags') + '</span>';
            return;
        }

        for (const tag of tags) {
            const isAvatar = tag.tagType === 'avatar';
            const isHtml = tag.tagType === 'html';

            if (isHtml) {
                // HTML tag: render in an inline wrapper with its dimensions
                const w = tag.htmlWidth || 120;
                const h = tag.htmlHeight || 40;
                const wrapper = document.createElement('span');
                wrapper.className = 'html-tag-badge';
                wrapper.style.cssText = `display:inline-flex;align-items:center;gap:2px;margin:0 4px 4px 0;vertical-align:middle;`;

                // 外层容器：固定宽高，overflow:hidden
                const htmlBox = document.createElement('span');
                htmlBox.style.cssText = `display:inline-block;width:${w}px;height:${h}px;overflow:hidden;vertical-align:middle;border:1px solid var(--border-color);border-radius:2px;`;
                // 内层：绝对定位，用于缩放
                const inner = document.createElement('div');
                inner.style.cssText = 'position:absolute;top:0;left:0;transform-origin:0 0;';
                htmlBox.style.position = 'relative';
                htmlBox.appendChild(inner);
                // scope
                const scopeId = 'dt-tag-scope-' + tag.id;
                htmlBox.setAttribute('data-tag-scope', scopeId);
                let processedCode = (typeof WailsBridge !== 'undefined' && WailsBridge.fixRelativeUrls)
                    ? WailsBridge.fixRelativeUrls(tag.htmlCode || '') : (tag.htmlCode || '');
                let scopedHtml = processedCode.replace(/<style([^>]*)>/g, (_, attrs) => {
                    return '<style' + attrs + ' data-scope="' + scopeId + '">';
                });
                inner.innerHTML = scopedHtml;
                inner.querySelectorAll('style[data-scope]').forEach(styleEl => {
                    const raw = styleEl.textContent;
                    if (!raw) return;
                    const scoped = raw.replace(/([^{}]*\{)/g, (rule) => {
                        const trimmed = rule.trim();
                        if (/^@|^\d+(\.\d+)?%|^(from|to)\b/i.test(trimmed)) return rule;
                        if (/\b(html|body|:root)\b|\*/.test(trimmed)) return '';
                        return rule.replace(/(^|,)\s*/g, (sep) => {
                            return sep + '[data-tag-scope="' + scopeId + '"] ';
                        });
                    });
                    styleEl.textContent = (typeof WailsBridge !== 'undefined' && WailsBridge.fixRelativeUrls) ? WailsBridge.fixRelativeUrls(scoped) : scoped;
                    styleEl.removeAttribute('data-scope');
                });
                inner.querySelectorAll('script').forEach(s => {
                    try { eval(s.textContent); } catch(e) {}
                });
                // 等比缩放
                requestAnimationFrame(() => {
                    const rect = inner.getBoundingClientRect();
                    const nw = rect.width || w;
                    const nh = rect.height || h;
                    const scale = Math.min(w / nw, h / nh, 1);
                    inner.style.transform = `scale(${scale})`;
                    const scaledW = nw * scale;
                    const scaledH = nh * scale;
                    inner.style.left = ((w - scaledW) / 2) + 'px';
                    inner.style.top = ((h - scaledH) / 2) + 'px';
                });

                const remove = document.createElement('span');
                remove.className = 'tag-remove';
                remove.innerHTML = '<span class="icon icon-close"></span>';
                remove.title = _t('detail.remove_tag');
                remove.addEventListener('click', async (e) => {
                    e.stopPropagation();
                    await Storage.removeTagFromImage(currentImage.path, tag.id);
                    await loadTagsAndFavorites(currentImage);
                });

                wrapper.appendChild(htmlBox);
                wrapper.appendChild(remove);
                imageTags.appendChild(wrapper);
            } else if (isAvatar) {
                // Avatar tag: match sidebar style
                const itemBg = tag.bgColor || 'var(--bg-secondary)';
                const shape = tag.shape || 'round';
                const rad = shape === 'sharp' ? 2 : shape === 'soft' ? 6 : 14;
                const thumbRad = shape === 'sharp' ? 2 : shape === 'soft' ? 4 : 10;

                const wrapper = document.createElement('span');
                wrapper.className = 'avatar-tag-badge';
                wrapper.style.cssText = `display:inline-flex;align-items:center;gap:0;margin:0 4px 4px 0;background:${itemBg};border:0.5px solid var(--border-color);border-radius:${rad}px;overflow:hidden;padding:0;height:24px;line-height:24px;`;

                if (tag.avatarData) {
                    const avImg = document.createElement('img');
                    avImg.src = (typeof WailsBridge !== 'undefined' && WailsBridge.getAvatarUrl)
                        ? WailsBridge.getAvatarUrl(tag.avatarData) : tag.avatarData;
                    avImg.style.cssText = `width:24px;height:24px;border-radius:${thumbRad}px 0 0 ${thumbRad}px;object-fit:cover;flex-shrink:0;`;
                    wrapper.appendChild(avImg);
                } else {
                    const avInit = document.createElement('span');
                    avInit.style.cssText = `width:24px;height:24px;border-radius:${thumbRad}px 0 0 ${thumbRad}px;display:flex;align-items:center;justify-content:center;font-size:12px;font-weight:500;flex-shrink:0;background:${itemBg};color:${tag.color || '#fff'};`;
                    avInit.textContent = (tag.name || '?')[0].toUpperCase();
                    wrapper.appendChild(avInit);
                }

                if (tag.showName !== false) {
                    const avName = document.createElement('span');
                    avName.textContent = tag.name;
                    avName.style.cssText = `font-size:10px;font-weight:500;padding:0 8px;white-space:nowrap;color:${tag.color || '#ffffff'};background:${itemBg};border-radius:0 ${rad}px ${rad}px 0;`;
                    wrapper.appendChild(avName);
                }

                const remove = document.createElement('span');
                remove.className = 'tag-remove';
                remove.style.cssText = 'cursor:pointer;font-size:10px;margin-left:2px;margin-right:4px;';
                remove.innerHTML = '<span class="icon icon-close"></span>';
                                remove.title = _t('detail.remove_tag');
                remove.addEventListener('click', async (e) => {
                    e.stopPropagation();
                    await Storage.removeTagFromImage(currentImage.path, tag.id);
                    await loadTagsAndFavorites(currentImage);
                });

                wrapper.appendChild(remove);
                imageTags.appendChild(wrapper);
            } else {
                const badge = document.createElement('span');
                badge.className = 'tag-badge';
                if (typeof TagStyle !== 'undefined') {
                    TagStyle.apply(badge, tag);
                } else {
                    badge.style.backgroundColor = tag.color || '#9b59b6';
                    badge.style.color = '#fff';
                }
                // 信息栏固定尺寸，不受左侧标签栏缩放影响
                badge.style.fontSize = '10px';
                badge.style.height = '24px';
                badge.style.lineHeight = '24px';

                // 浅色主题下纯图标标签文字改为深色
                if (tag.iconOnly && document.documentElement.getAttribute('data-theme') === 'light') {
                    badge.style.color = '#1a1a2e';
                }

                const isIconOnly = tag.iconOnly || (!tag.name && tag.icon);
                if (isIconOnly) {
                    badge.style.padding = '0';
                }
                if (tag.icon) {
                    if (tag.icon.indexOf('data:') === 0) {
                        const iconImg = document.createElement('img');
                        iconImg.className = 'tag-icon-img';
                        iconImg.src = tag.icon;
                        iconImg.alt = '';
                        iconImg.style.height = isIconOnly ? '24px' : '10px';
                        iconImg.style.verticalAlign = 'middle';
                        badge.appendChild(iconImg);
                    } else {
                        const iconSpan = document.createElement('span');
                        iconSpan.textContent = tag.icon;
                        iconSpan.style.fontSize = isIconOnly ? '24px' : '10px';
                        iconSpan.style.verticalAlign = 'middle';
                        iconSpan.style.lineHeight = '24px';
                        badge.appendChild(iconSpan);
                    }
                }

                if (tag.showName !== false) {
                    const label = document.createElement('span');
                    label.textContent = tag.name;
                    label.style.lineHeight = '24px';
                    badge.appendChild(label);
                }

                const remove = document.createElement('span');
                remove.className = 'tag-remove';
                remove.innerHTML = '<span class="icon icon-close"></span>';
                                remove.title = _t('detail.remove_tag');
                remove.addEventListener('click', async (e) => {
                    e.stopPropagation();
                    await Storage.removeTagFromImage(currentImage.path, tag.id);
                    await loadTagsAndFavorites(currentImage);
                });

                badge.appendChild(remove);
                imageTags.appendChild(badge);
            }
        }
    }

    async function addTagToCurrentImage() {
        if (!currentImage) return;
        const tagId = tagSelect.value;
        if (!tagId) {
            App.showToast(_t('detail.select_tag'), 'warning');
            return;
        }

        await Storage.addTagToImage(currentImage.path, tagId);
        await loadTagsAndFavorites(currentImage);
        App.showToast(_t('detail.tag_added'), 'success');
    }

    async function toggleFavoriteCurrentImage() {
        if (!currentImage) return;
        const isFav = await Storage.toggleFavorite(currentImage.path);
        btnToggleFavorite.innerHTML = isFav ? '<span class="icon icon-favorite-on"></span> ' + _t('detail.unfavorite') : '<span class="icon icon-favorite-off"></span> ' + _t('detail.favorite');
        btnToggleFavorite.classList.toggle('btn-danger', isFav);
        App.showToast(isFav ? _t('detail.favorited') : _t('detail.unfavorited'), 'success');

        // 刷新画廊中的收藏图标
        Gallery.render();
    }

    // ==================== 工具函数 ====================

    function copyToClipboard(text, label) {
        if (!text || text === _t('detail.none') || text === _t('detail.no_prompt')) return;

        navigator.clipboard.writeText(text).then(() => {
            App.showToast(_t('detail.copied') + ': ' + label, 'success');
        }).catch(() => {
            // 降级方案
            const textarea = document.createElement('textarea');
            textarea.value = text;
            textarea.style.position = 'fixed';
            textarea.style.opacity = '0';
            document.body.appendChild(textarea);
            textarea.select();
            document.execCommand('copy');
            document.body.removeChild(textarea);
            App.showToast(`已复制: ${label}`, 'success');
        });
    }

    function escapeHtml(str) {
        const div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    }

    // ==================== 公开 API ====================

    // 监听标签变更事件，刷新信息栏中的标签显示
    window.addEventListener('tags-changed', () => {
        if (currentImage) loadTagsAndFavorites(currentImage);
    });

    return {
        init,
        showImage,
        hideImage,
        togglePanel,
        expand,
        collapse,
        setLocked,
        getLocked,
        getCurrentImage: () => currentImage,
        refreshDetailApiConfigSelect
    };
})();

// ============================================================
// ImageViewer - 大图查看器（高性能版）
//   核心优化：
//   1. 图片降采样 - 使用 Canvas 创建降采样版本，避免加载原始大图
//   2. 滚轮缩放 RAF 合并 - 使用 requestAnimationFrame 合并滚轮事件
//   3. image-rendering 优化 - 缩放时使用更快的插值算法
//   4. decoding="async" - 异步解码不阻塞主线程
//   5. CSS will-change + contain - GPU 合成层优化
// ============================================================
const ImageViewer = (() => {
    const _t = (typeof I18n !== "undefined" ? I18n.t : (s) => s);
    let overlay, wrapper, img, closeBtn, prevBtn, nextBtn;
    let filenameEl, positionEl, zoomLevelEl;
    let videoEl = null;
    let scale = 1;
    let rotation = 0;
    let isDragging = false;
    let dragStartX, dragStartY, offsetX = 0, offsetY = 0;
    let naturalWidth = 0, naturalHeight = 0;

    const WHEEL_SENSITIVITY = 0.0012;   // 滚轮灵敏度
    const MIN_SCALE = 0.1;              // 最小缩放
    const MAX_SCALE = 50;               // 最大缩放

    // RAF 节流
    let rafPending = false;

    // 当前图片列表和索引
    let imageList = [];
    let currentIndex = -1;
    let currentImgData = null;

    // 幻灯片
    let slideshowTimer = null;
    let slideshowPlaying = false;
    let slideshowShortcut;
    let intervalStepper, intervalVal, intervalMinus, intervalPlus;
    let slideshowIntervalMs = 2000;
    let filmstrip, filmstripList;
    let filmstripVisible = false;

    // 全屏
    let isFullscreen = false;
    let showOriginalSize = false;
    let scRotate, scReset, scPrev, scNext, scFullscreen, scClose, scFilmstrip, scOriginal, scParams;
    let _viewerInitialized = false;

    function init() {
        if (_viewerInitialized) return;
        _viewerInitialized = true;

        overlay = document.getElementById('imageViewerOverlay');
        wrapper = document.getElementById('imageViewerWrapper');
        img = document.getElementById('imageViewerImg');
        closeBtn = document.getElementById('imageViewerClose');
        prevBtn = document.getElementById('imageViewerPrev');
        nextBtn = document.getElementById('imageViewerNext');
        filenameEl = document.getElementById('imageViewerFilename');
        positionEl = document.getElementById('imageViewerPosition');
        zoomLevelEl = document.getElementById('imageViewerZoomLevel');
        slideshowShortcut = document.getElementById('imageViewerSlideshowShortcut');
        intervalStepper = document.getElementById('imageViewerIntervalStepper');
        intervalVal = document.getElementById('intervalVal');
        intervalMinus = document.getElementById('intervalMinus');
        intervalPlus = document.getElementById('intervalPlus');
        filmstrip = document.getElementById('imageViewerFilmstrip');
        filmstripList = document.getElementById('imageViewerFilmstripList');

        // 设置 transform-origin 为图片中心，缩放以中心为原点
        img.style.transformOrigin = 'center center';

        // 设置 decoding 为 async，不阻塞主线程
        img.decoding = 'async';

        bindEvents();
        initIdleTimer();
    }

    // ==================== 全屏鼠标空闲自动隐藏控件 ====================

    let idleTimer = null;
    const IDLE_DELAY = 1500;

    function initIdleTimer() {
        const container = document.getElementById('imageViewerContainer');
        if (!container) return;

        function resetIdle() {
            container.classList.remove('idle');
            clearTimeout(idleTimer);
            idleTimer = setTimeout(() => {
                container.classList.add('idle');
            }, IDLE_DELAY);
        }

        container.addEventListener('mousemove', resetIdle);
        container.addEventListener('click', resetIdle);
        container.addEventListener('wheel', resetIdle);
        container.addEventListener('pointerdown', resetIdle);

        container.addEventListener('mouseleave', () => {
            clearTimeout(idleTimer);
        });
    }

    function bindEvents() {
        // 关闭按钮
        closeBtn.addEventListener('click', close);
        // 点击 overlay 背景或容器空白区域关闭（不拦截图片上的点击）
        overlay.addEventListener('click', (e) => {
            if (e.target === overlay || e.target === wrapper) close();
        });

        // 右键菜单 - 大图查看器
        img.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            if (currentImgData) {
                ImageContextMenu.show(e.clientX, e.clientY, currentImgData.path, currentImgData.rootPath, currentImgData.folder);
            }
        });

        // 导航按钮
        prevBtn.addEventListener('click', () => { stopSlideshow(); navigate(-1); });
        nextBtn.addEventListener('click', () => { stopSlideshow(); navigate(1); });

        // 底部快捷键按钮
        if (slideshowShortcut) slideshowShortcut.addEventListener('click', toggleSlideshow);
        // 间隔步进器
        const INTERVAL_STEPS = [1000, 2000, 3000, 5000, 10000, 15000, 30000, 60000];
        if (intervalMinus) intervalMinus.addEventListener('click', (e) => {
            e.stopPropagation();
            const idx = INTERVAL_STEPS.indexOf(slideshowIntervalMs);
            if (idx > 0) slideshowIntervalMs = INTERVAL_STEPS[idx - 1];
            updateIntervalDisplay();
        });
        if (intervalPlus) intervalPlus.addEventListener('click', (e) => {
            e.stopPropagation();
            const idx = INTERVAL_STEPS.indexOf(slideshowIntervalMs);
            if (idx < INTERVAL_STEPS.length - 1) slideshowIntervalMs = INTERVAL_STEPS[idx + 1];
            updateIntervalDisplay();
        });
        scRotate = document.getElementById('shortcutRotate');
        scReset = document.getElementById('shortcutReset');
        scPrev = document.getElementById('shortcutPrev');
        scNext = document.getElementById('shortcutNext');
        scFullscreen = document.getElementById('shortcutFullscreen');
        scClose = document.getElementById('shortcutClose');
        if (scRotate) scRotate.addEventListener('click', () => rotate(90));
        if (scReset) scReset.addEventListener('click', resetTransform);
        if (scPrev) scPrev.addEventListener('click', () => { stopSlideshow(); navigate(-1); });
        if (scNext) scNext.addEventListener('click', () => { stopSlideshow(); navigate(1); });
        if (scFullscreen) scFullscreen.addEventListener('click', toggleFullscreen);
        if (scClose) scClose.addEventListener('click', () => { stopSlideshow(); close(); });
        scFilmstrip = document.getElementById('shortcutFilmstrip');
        if (scFilmstrip) scFilmstrip.addEventListener('click', toggleFilmstrip);
        scOriginal = document.getElementById('shortcutOriginal');
        if (scOriginal) scOriginal.addEventListener('click', toggleOriginalSize);
        scParams = document.getElementById('shortcutParams');
        if (scParams) scParams.addEventListener('click', toggleParams);

        document.addEventListener('keydown', (e) => {
            if (!overlay || overlay.style.display !== 'flex') return;
            const key = e.key;
            const activeEl = document.activeElement;
            if (activeEl && (activeEl.tagName === 'INPUT' || activeEl.tagName === 'TEXTAREA' || activeEl.isContentEditable)) return;
            if (key === 'Escape') { e.preventDefault(); stopSlideshow(); close(); return; }
            if (key === 'ArrowLeft' || key === 'a' || key === 'A') { e.preventDefault(); stopSlideshow(); navigate(-1); return; }
            if (key === 'ArrowRight' || key === 'd' || key === 'D') { e.preventDefault(); stopSlideshow(); navigate(1); return; }
            if (key === ' ') {
                if (currentImgData && currentImgData.isVideo) return;
                e.preventDefault();
                toggleSlideshow();
                return;
            }
            if (key === 'f' || key === 'F') { e.preventDefault(); toggleFullscreen(); return; }
            if (key === 't' || key === 'T') { e.preventDefault(); toggleFilmstrip(); return; }
            if (key === 'p' || key === 'P') { e.preventDefault(); toggleParams(); return; }
            if (key === 'r' || key === 'R') { e.preventDefault(); rotate(90); return; }
            if (key === '1') { e.preventDefault(); toggleOriginalSize(); return; }
            if (key === '0') { e.preventDefault(); resetTransform(); return; }
        });

        // 滚轮缩放（以鼠标位置为基准）
        wrapper.addEventListener('wheel', (e) => {
            e.preventDefault();

            // 标准化 delta
            let normalizedDelta = e.deltaY;
            if (e.deltaMode === 1) {
                normalizedDelta *= 35;
            } else if (e.deltaMode === 2) {
                normalizedDelta *= 600;
            }

            // 记录鼠标相对于 wrapper 中心的位置，用于缩放后保持鼠标下方内容不变
            const rect = wrapper.getBoundingClientRect();
            const cx = rect.left + rect.width / 2;
            const cy = rect.top + rect.height / 2;
            const oldScale = scale;

            // 指数映射
            const factor = Math.exp(-normalizedDelta * WHEEL_SENSITIVITY);
            scale = Math.max(MIN_SCALE, Math.min(MAX_SCALE, scale * factor));

            // 以鼠标位置为基准调整偏移，使缩放时鼠标所指的图片位置保持不变
            if (oldScale > 0.001) {
                const ratio = scale / oldScale;
                offsetX = e.clientX - cx - (e.clientX - cx - offsetX) * ratio;
                offsetY = e.clientY - cy - (e.clientY - cy - offsetY) * ratio;
            }

            // RAF 合并：如果已经有待处理的 RAF，不再重复调度
            if (!rafPending) {
                rafPending = true;
                requestAnimationFrame(() => {
                    rafPending = false;
                    applyTransform();
                });
            }
        }, { passive: false });

        // 鼠标拖拽平移
        img.addEventListener('mousedown', (e) => {
            if (e.button !== 0) return;
            isDragging = true;
            dragStartX = e.clientX - offsetX;
            dragStartY = e.clientY - offsetY;
            img.style.cursor = 'grabbing';
            e.preventDefault();
        });

        document.addEventListener('mousemove', (e) => {
            if (!isDragging) return;
            offsetX = e.clientX - dragStartX;
            offsetY = e.clientY - dragStartY;
            // 拖拽时也使用 RAF 合并
            if (!rafPending) {
                rafPending = true;
                requestAnimationFrame(() => {
                    rafPending = false;
                    applyTransform();
                });
            }
        });

        document.addEventListener('mouseup', () => {
            if (isDragging) {
                isDragging = false;
                img.style.cursor = 'grab';
            }
        });

        // 图片加载完成后记录自然尺寸，并执行 fitToContainer
        img.addEventListener('load', () => {
            naturalWidth = img.naturalWidth;
            naturalHeight = img.naturalHeight;
            img.style.visibility = 'visible';
            // 加载完成后根据原图模式决定缩放
            if (showOriginalSize) {
                applyOriginalSize();
            } else {
                fitToContainer();
            }
        });

        // ==================== 触摸手势 ====================
        // 支持: 单指滑动切换上下张 | 单指拖拽平移 | 双指缩放（跟手比例映射）
        let touchStartTime = 0;
        let touchStartX = 0, touchStartY = 0;
        let touchMoved = false;
        let touchIsSwipe = false;
        let touchPinch = false;
        let lastTouchDist = 0;
        let pinchStartDist = 0;     // 双指缩放起始距离
        let pinchStartScale = 1;    // 双指缩放起始 scale
        let pinchStartOffsetX = 0, pinchStartOffsetY = 0; // 双指缩放起始偏移
        let pinchMidX = 0, pinchMidY = 0;                 // 双指缩放中点（wrapper 坐标）
        let touchPanStartOffsetX = 0, touchPanStartOffsetY = 0;

        const SWIPE_THRESHOLD = 60;
        const SWIPE_VELOCITY = 0.4;
        const SWIPE_DIRECTION_RATIO = 1.5;

        wrapper.addEventListener('touchstart', (e) => {
            if (e.touches.length === 2) {
                touchPinch = true;
                touchIsSwipe = false;
                touchMoved = true;
                pinchStartDist = Math.hypot(
                    e.touches[0].clientX - e.touches[1].clientX,
                    e.touches[0].clientY - e.touches[1].clientY
                );
                pinchStartScale = scale;
                pinchStartOffsetX = offsetX;
                pinchStartOffsetY = offsetY;
                const rect = wrapper.getBoundingClientRect();
                pinchMidX = (e.touches[0].clientX + e.touches[1].clientX) / 2 - rect.left;
                pinchMidY = (e.touches[0].clientY + e.touches[1].clientY) / 2 - rect.top;
            } else if (e.touches.length === 1 && !touchPinch) {
                touchStartTime = Date.now();
                touchStartX = e.touches[0].clientX;
                touchStartY = e.touches[0].clientY;
                touchMoved = false;
                touchIsSwipe = false;
                touchPanStartOffsetX = offsetX;
                touchPanStartOffsetY = offsetY;
            }
        }, { passive: true });

        wrapper.addEventListener('touchmove', (e) => {
            if (e.touches.length === 2 && touchPinch) {
                e.preventDefault();
                const dist = Math.hypot(
                    e.touches[0].clientX - e.touches[1].clientX,
                    e.touches[0].clientY - e.touches[1].clientY
                );

                if (pinchStartDist < 5) return;

                // 基于初始距离的比例映射：手指捏合多少，缩放就变化多少
                const targetScale = pinchStartScale * (dist / pinchStartDist);
                const clampedScale = Math.max(MIN_SCALE, Math.min(MAX_SCALE, targetScale));

                // 以 pinch 起始中点为锚进行缩放偏移计算
                // 使双指中点对准的图片像素在缩放前后保持不变
                const cx = wrapper.clientWidth / 2;
                const cy = wrapper.clientHeight / 2;
                const ratio = clampedScale / pinchStartScale;
                offsetX = pinchStartOffsetX + (pinchMidX - cx - pinchStartOffsetX) * (1 - ratio);
                offsetY = pinchStartOffsetY + (pinchMidY - cy - pinchStartOffsetY) * (1 - ratio);
                scale = clampedScale;

                if (!rafPending) {
                    rafPending = true;
                    requestAnimationFrame(() => {
                        rafPending = false;
                        applyTransform();
                    });
                }
            } else if (e.touches.length === 1 && !touchPinch) {
                const dx = e.touches[0].clientX - touchStartX;
                const dy = e.touches[0].clientY - touchStartY;

                if (!touchMoved && !touchIsSwipe) {
                    if (Math.abs(dx) > 5 || Math.abs(dy) > 5) {
                        touchMoved = true;
                        const absDx = Math.abs(dx);
                        const absDy = Math.abs(dy);

                        if (absDx > absDy * SWIPE_DIRECTION_RATIO) {
                            touchIsSwipe = true;
                        }
                    }
                }

                if (touchIsSwipe) {
                    e.preventDefault();
                } else if (touchMoved) {
                    e.preventDefault();
                    offsetX = touchPanStartOffsetX + dx;
                    offsetY = touchPanStartOffsetY + dy;
                    if (!rafPending) {
                        rafPending = true;
                        requestAnimationFrame(() => {
                            rafPending = false;
                            applyTransform();
                        });
                    }
                }
            }
        }, { passive: false });

        wrapper.addEventListener('touchend', (e) => {
            if (touchPinch) {
                if (e.touches.length === 0) {
                    touchPinch = false;
                    touchMoved = false;
                } else if (e.touches.length === 1) {
                    touchPinch = false;
                    touchStartX = e.touches[0].clientX;
                    touchStartY = e.touches[0].clientY;
                    touchMoved = false;
                    touchIsSwipe = false;
                    touchPanStartOffsetX = offsetX;
                    touchPanStartOffsetY = offsetY;
                }
                return;
            }

            if (touchIsSwipe) {
                const dx = touchStartX - (e.changedTouches[0] ? e.changedTouches[0].clientX : touchStartX);
                const elapsed = Date.now() - touchStartTime;
                const velocity = elapsed > 0 ? Math.abs(dx) / elapsed : 0;

                if (Math.abs(dx) >= SWIPE_THRESHOLD || velocity >= SWIPE_VELOCITY) {
                    navigate(dx > 0 ? 1 : -1);
                }
            }

            touchMoved = false;
            touchIsSwipe = false;
            touchPinch = false;
        });
    }

    function navigate(direction) {
        let newIndex = currentIndex + direction;
        // 循环浏览：到最后时返回第一张，到第一张时返回最后一张
        if (newIndex < 0) newIndex = imageList.length - 1;
        if (newIndex >= imageList.length) newIndex = 0;

        // 释放当前 blob URL 和降采样资源
        releaseCurrentImage();

        currentIndex = newIndex;
        currentImgData = imageList[currentIndex];
        loadImage(currentImgData);
        if (filmstripVisible) updateFilmstripCurrent();

        // 预解析元数据并刷新浮动面板
        // ★ 等图片加载完成（naturalWidth/naturalHeight 就绪）后再渲染
        if (typeof Gallery !== 'undefined' && Gallery.resolveMetadataOnDemand) {
            Gallery.resolveMetadataOnDemand(currentImgData).then(() => {
                if (!paramsPanelVisible) return;
                if (naturalWidth > 0 && naturalHeight > 0) {
                    renderParamsPanel(currentImgData);
                } else {
                    // 图片还没加载完，等 load 事件后再渲染
                    const onLoad = () => {
                        img.removeEventListener('load', onLoad);
                        renderParamsPanel(currentImgData);
                    };
                    img.addEventListener('load', onLoad);
                }
            });
        }
    }

    // ==================== 幻灯片 ====================

    function updateIntervalDisplay() {
        if (intervalVal) intervalVal.textContent = (slideshowIntervalMs / 1000) + 's';
    }

    function toggleSlideshow() {
        if (slideshowPlaying) {
            stopSlideshow();
        } else {
            startSlideshow();
        }
    }

    function startSlideshow() {
        if (slideshowPlaying) return;
        slideshowPlaying = true;
        const shortcuts = document.getElementById('imageViewerShortcuts');
        if (shortcuts) shortcuts.classList.add('slideshow-active');
        if (slideshowShortcut) slideshowShortcut.classList.add('playing');
        if (intervalStepper) intervalStepper.classList.add('visible');
        scheduleNext();
    }

    function stopSlideshow() {
        slideshowPlaying = false;
        if (slideshowTimer) {
            clearTimeout(slideshowTimer);
            slideshowTimer = null;
        }
        const shortcuts = document.getElementById('imageViewerShortcuts');
        if (shortcuts) shortcuts.classList.remove('slideshow-active');
        if (slideshowShortcut) slideshowShortcut.classList.remove('playing');
        if (intervalStepper) intervalStepper.classList.remove('visible');
    }

async function toggleFullscreen() {
	isFullscreen = !isFullscreen;
	const scFs = document.getElementById('shortcutFullscreen');
	const container = document.getElementById('imageViewerContainer');
	try {
		if (isFullscreen) {
			if (window.runtime && window.runtime.WindowFullscreen) {
				await window.runtime.WindowFullscreen();
			} else if (document.fullscreenEnabled) {
				await document.documentElement.requestFullscreen();
			}
			container.classList.add('fullscreen');
			if (scFs) scFs.classList.add('active');
		} else {
			if (window.runtime && window.runtime.WindowUnfullscreen) {
				await window.runtime.WindowUnfullscreen();
			} else if (document.fullscreenElement) {
				await document.exitFullscreen();
			}
			container.classList.remove('fullscreen');
			if (scFs) scFs.classList.remove('active');
		}
	} catch (e) {
		isFullscreen = !isFullscreen;
		console.warn('[Detail] 全屏切换失败:', e);
	}
}

    // ==================== 缩略图栏 ====================

    function toggleFilmstrip() {
        filmstripVisible = !filmstripVisible;
        if (filmstripVisible) {
            buildFilmstrip();
            filmstrip.style.display = '';
            document.getElementById('imageViewerContainer').classList.add('has-filmstrip');
            // 绑定分页加载滚动监听
            filmstripList.addEventListener('scroll', checkFilmstripLoadMore, { passive: true });
        } else {
            filmstrip.style.display = 'none';
            document.getElementById('imageViewerContainer').classList.remove('has-filmstrip');
            filmstripList.removeEventListener('scroll', checkFilmstripLoadMore);
        }
        const btn = document.getElementById('shortcutFilmstrip');
        if (btn) btn.classList.toggle('active', filmstripVisible);
    }

    function buildFilmstrip() {
        if (!filmstripList) return;
        filmstripList.innerHTML = '';
        const frag = document.createDocumentFragment();
        for (let i = 0; i < imageList.length; i++) {
            frag.appendChild(createFilmstripItem(i));
        }
        filmstripList.appendChild(frag);
        requestAnimationFrame(() => updateFilmstripCurrent());
    }

    function createFilmstripItem(i) {
        const img = imageList[i];
        const item = document.createElement('div');
        item.className = 'image-viewer-filmstrip-item';
        if (i === currentIndex) item.classList.add('current');
        item.title = img.name;
        const thumb = document.createElement('img');
        thumb.src = img.thumbnailUrl || img.url;
        thumb.alt = img.name;
        thumb.loading = 'lazy';
        item.appendChild(thumb);
        item.addEventListener('click', () => {
            stopSlideshow();
            releaseCurrentImage();
            currentIndex = i;
            currentImgData = imageList[i];
            loadImage(currentImgData);
            updateFilmstripCurrent();
        });
        return item;
    }

    function appendFilmstripItems(startIndex) {
        if (!filmstripList) return;
        const frag = document.createDocumentFragment();
        for (let i = startIndex; i < imageList.length; i++) {
            frag.appendChild(createFilmstripItem(i));
        }
        filmstripList.appendChild(frag);
    }

    function checkFilmstripLoadMore() {
        if (!filmstripList || !filmstripVisible) return;
        const scrollBottom = filmstripList.scrollTop + filmstripList.clientHeight;
        if (scrollBottom >= filmstripList.scrollHeight - 200) {
            const prevLen = imageList.length;
            const hasMore = typeof Gallery !== 'undefined' && Gallery.hasMoreImages ? Gallery.hasMoreImages() : false;
            if (!hasMore) return;
            if (typeof Gallery !== 'undefined' && Gallery.triggerLoadMore) {
                Gallery.triggerLoadMore().then(() => {
                    if (imageList.length > prevLen) {
                        appendFilmstripItems(prevLen);
                    }
                }).catch(() => {});
            }
        }
    }

    function updateFilmstripCurrent() {
        if (!filmstripList || !filmstripVisible) return;
        const items = filmstripList.querySelectorAll('.image-viewer-filmstrip-item');
        items.forEach((item, i) => item.classList.toggle('current', i === currentIndex));
        const current = items[currentIndex];
        if (current) {
            current.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
        }
    }

    function scheduleNext() {
        if (!slideshowPlaying) return;
        const interval = slideshowIntervalMs;
        slideshowTimer = setTimeout(() => {
            if (!slideshowPlaying) return;
            // 到达末尾时循环到开头
            if (currentIndex + 1 >= imageList.length) {
                if (imageList.length === 0) { stopSlideshow(); return; }
                currentIndex = -1;
            }
            navigate(1);
            scheduleNext();
        }, interval);
    }

    function loadImage(imgData) {
        rotation = 0;
        offsetX = 0;
        offsetY = 0;
        naturalWidth = 0;
        naturalHeight = 0;

        filenameEl.textContent = imgData.name || '';
        updatePosition();
        updateNavButtons();

        if (imgData.isVideo) {
            // 隐藏图片，显示视频
            img.style.visibility = 'hidden';
            img.style.display = 'none';
            if (!videoEl) {
                videoEl = document.createElement('video');
                videoEl.controls = true;
                videoEl.style.position = 'absolute';
                videoEl.style.maxWidth = '100%';
                videoEl.style.maxHeight = '100%';
                videoEl.style.top = '50%';
                videoEl.style.left = '50%';
                videoEl.style.transform = 'translate(-50%, -50%)';
                videoEl.style.background = '#000';
                videoEl.style.borderRadius = 'var(--radius)';
                videoEl.preload = 'auto';
                videoEl.onerror = function() {
                    console.warn('[Viewer] 视频无法播放');
                    videoEl.style.display = 'none';
                };
                wrapper.appendChild(videoEl);
            }
            videoEl.style.display = '';
            videoEl.src = imgData.url || '';
            scale = 1;
            applyTransform();
            img.style.cursor = 'default';
        } else {
            // 显示图片，隐藏视频
            if (videoEl) {
                videoEl.pause();
                videoEl.style.display = 'none';
            }
            img.style.display = '';
            img.style.visibility = 'hidden';

            let src = imgData.url || imgData.thumbnailUrl;
            if (imgData.file) {
                src = URL.createObjectURL(imgData.file);
            }

            img.src = src;
            img.alt = imgData.name || '';
            img.style.cursor = 'grab';
        }
    }

    function releaseCurrentImage() {
        if (img.src && img.src.startsWith('blob:')) {
            URL.revokeObjectURL(img.src);
        }
        if (videoEl) {
            videoEl.pause();
            videoEl.src = '';
        }
    }

    function updatePosition() {
        if (imageList.length > 1) {
            positionEl.textContent = `${currentIndex + 1} / ${imageList.length}`;
        } else {
            positionEl.textContent = '';
        }
    }

    function updateNavButtons() {
        if (imageList.length <= 1) {
            prevBtn.style.display = 'none';
            nextBtn.style.display = 'none';
        } else {
            prevBtn.style.display = '';
            nextBtn.style.display = '';
            // 循环浏览模式下按钮始终启用
            prevBtn.disabled = false;
            nextBtn.disabled = false;
        }
    }

    function calculateFitScale() {
        if (!naturalWidth || !naturalHeight) return 1;
        const fitW = wrapper.clientWidth / naturalWidth;
        const fitH = wrapper.clientHeight / naturalHeight;
        return Math.min(fitW, fitH);
    }

    function fitToContainer() {
        if (!naturalWidth || !naturalHeight) return;
        scale = calculateFitScale();
        rotation = 0;
        offsetX = 0;
        offsetY = 0;
        applyTransform();
    }

    function open(imgData) {
        if (!imgData) return;

        // 获取当前画廊显示的图片列表
        imageList = typeof Gallery !== 'undefined' && Gallery.getImages ? Gallery.getImages() : [imgData];
        currentIndex = imageList.findIndex(img => img.path === imgData.path);
        if (currentIndex === -1) {
            imageList = [imgData];
            currentIndex = 0;
        }
        currentImgData = imgData;

        // 初始状态：scale 设为 0，等图片加载完成后由 fitToContainer 自动适配
        // 避免先显示原始大小再跳转到适配大小造成的闪烁
        scale = 0;
        rotation = 0;
        offsetX = 0;
        offsetY = 0;
        naturalWidth = 0;
        naturalHeight = 0;

        overlay.style.display = 'flex';
        loadImage(imgData);

        // 预解析元数据（用于 P 键参数面板）
        if (!imgData.metadata && typeof Gallery !== 'undefined' && Gallery.resolveMetadataOnDemand) {
            Gallery.resolveMetadataOnDemand(imgData);
        }

        // 重建缩略图栏
        if (filmstripVisible) buildFilmstrip();

        // 防止 body 滚动
        document.body.style.overflow = 'hidden';
    }

    function close() {
        stopSlideshow();
        if (isFullscreen) {
            try { window.runtime.WindowUnfullscreen(); } catch (e) {}
            document.getElementById('imageViewerContainer').classList.remove('fullscreen');
            const scFs = document.getElementById('shortcutFullscreen');
            if (scFs) scFs.classList.remove('active');
            isFullscreen = false;
        }
        // 关闭前把图廊滚动到当前图片
        if (currentImgData && typeof Gallery !== 'undefined' && Gallery.scrollToImage) {
            Gallery.scrollToImage(currentImgData.path);
        }
        overlay.style.display = 'none';
        // 释放所有 blob URL 和降采样资源
        releaseCurrentImage();
        img.src = '';
        img.style.display = '';
        if (videoEl) videoEl.style.display = 'none';
        imageList = [];
        currentIndex = -1;
        currentImgData = null;
        filmstripVisible = false;
        if (filmstrip) filmstrip.style.display = 'none';
        document.getElementById('imageViewerContainer').classList.remove('has-filmstrip');
        const scFilmstrip = document.getElementById('shortcutFilmstrip');
        if (scFilmstrip) scFilmstrip.classList.remove('active');
        document.body.style.overflow = '';
    }

    function rotate(deg) {
        rotation = (rotation + deg) % 360;
        applyTransform();
    }

    function toggleOriginalSize() {
        showOriginalSize = !showOriginalSize;
        const btn = document.getElementById('shortcutOriginal');
        if (showOriginalSize) {
            applyOriginalSize();
            if (btn) btn.classList.add('active');
        } else {
            fitToContainer();
            if (btn) btn.classList.remove('active');
        }
    }

    let paramsPanelVisible = false;
    let paramsPanelEl = null;

    async function toggleParams() {
        if (!paramsPanelEl) {
            paramsPanelEl = document.createElement('div');
            paramsPanelEl.className = 'image-viewer-params-panel';
            paramsPanelEl.innerHTML = '<div class="iv-params-header"><span>' + _t('detail.photo_info') + '</span><button class="iv-params-close" id="ivParamsClose">×</button></div><div class="iv-params-body" id="ivParamsBody"></div>';
            wrapper.appendChild(paramsPanelEl);
            document.getElementById('ivParamsClose').addEventListener('click', (e) => {
                e.stopPropagation();
                toggleParams();
            });
        }

        paramsPanelVisible = !paramsPanelVisible;
        paramsPanelEl.style.display = paramsPanelVisible ? 'block' : 'none';
        const btn = document.getElementById('shortcutParams');
        if (btn) btn.classList.toggle('active', paramsPanelVisible);

        console.log('[ImageViewer] toggleParams called, paramsPanelVisible:', paramsPanelVisible, 'currentImgData:', !!currentImgData, 'currentImgData.name:', currentImgData ? currentImgData.name : 'null');
        if (paramsPanelVisible && currentImgData) {
            // 按需解析元数据
            if (!currentImgData.metadata && typeof Gallery !== 'undefined' && Gallery.resolveMetadataOnDemand) {
                await Gallery.resolveMetadataOnDemand(currentImgData);
            }
            renderParamsPanel(currentImgData);
        }
    }

    function renderParamsPanel(imgData) {
        const body = document.getElementById('ivParamsBody');
        if (!body) return;
        console.log('[renderParamsPanel] imgData:', JSON.stringify({ name: imgData.name, size: imgData.size, width: imgData.width, height: imgData.height, createdAt: imgData.createdAt, lastModified: imgData.lastModified, path: imgData.path, hasMetadata: !!imgData.metadata }));
        const meta = imgData.metadata;
        const raw = meta && meta.raw ? meta.raw : null;
        const params = meta && meta.params ? meta.params : null;
        const prompt = meta && meta.prompt ? meta.prompt : '';
        const negPrompt = meta && meta.negativePrompt ? meta.negativePrompt : '';
        const exifKeys = [_t('detail.photo_camera_make'), _t('detail.photo_camera_model'), _t('detail.photo_capture_time'), _t('detail.photo_exposure'), _t('detail.photo_aperture'), _t('detail.photo_iso'), _t('detail.photo_focal'), _t('detail.photo_lens'), _t('detail.photo_flash')];
        const hasExif = raw && exifKeys.some(k => raw[k]);
        const hasParams = params && Object.keys(params).length > 0;
        const hasPrompt = prompt || negPrompt;

        body.innerHTML = '';

        let currentSection = null;

        const newSection = () => {
            currentSection = document.createElement('div');
            currentSection.className = 'iv-params-section';
            body.appendChild(currentSection);
        };

        const addRow = (label, value) => {
            if (!currentSection) newSection();
            const row = document.createElement('div');
            row.className = 'iv-params-row';
            const spanL = document.createElement('span');
            spanL.textContent = label;
            const spanV = document.createElement('span');
            spanV.textContent = value;
            row.appendChild(spanL);
            row.appendChild(spanV);
            currentSection.appendChild(row);
        };

        // 文件信息
        let sizeStr = '?';
        if (imgData.size) {
            const kb = imgData.size / 1024;
            sizeStr = kb < 1 ? `${imgData.size} B` : kb < 1024 ? `${kb.toFixed(1)} KB` : `${(imgData.size / (1024 * 1024)).toFixed(2)} MB`;
        }
        const modTime = imgData.lastModified ? new Date(imgData.lastModified).toLocaleString() : '?';
        const createTime = (imgData.createdAt && imgData.createdAt > 0) ? new Date(imgData.createdAt).toLocaleString() : (imgData.lastModified ? new Date(imgData.lastModified).toLocaleString() + ' (' + _t('detail.photo_mod_time_label') + ')' : '?');
        const w = imgData.width || naturalWidth;
        const h = imgData.height || naturalHeight;
        const dims = (w && h) ? `${w} x ${h}` : '?';
        const ext = imgData.name ? imgData.name.split('.').pop().toUpperCase() : '?';

        newSection();
        addRow(_t('detail.photo_name'), imgData.name || '?');
        addRow(_t('detail.photo_type'), ext);
        addRow(_t('detail.photo_size'), sizeStr);
        addRow(_t('detail.photo_dimensions'), dims);
        addRow(_t('detail.photo_mod_time'), modTime);
        addRow(_t('detail.photo_created_time'), createTime);

        // 提示词
        if (hasPrompt) {
            newSection();
            if (prompt) {
                const p = document.createElement('div');
                p.className = 'iv-params-text';
                const pl = document.createElement('div');
                pl.className = 'iv-params-text-label';
                pl.textContent = _t('detail.positive_prompt');
                p.appendChild(pl);
                const pv = document.createElement('div');
                pv.textContent = prompt;
                p.appendChild(pv);
                currentSection.appendChild(p);
            }
            if (negPrompt) {
                const n = document.createElement('div');
                n.className = 'iv-params-text';
                const nl = document.createElement('div');
                nl.className = 'iv-params-text-label';
                nl.textContent = _t('detail.negative_prompt');
                n.appendChild(nl);
                const nv = document.createElement('div');
                nv.textContent = negPrompt;
                n.appendChild(nv);
                currentSection.appendChild(n);
            }
        }

        // 相机 EXIF
        if (hasExif) {
            newSection();
            for (const key of [_t('detail.photo_capture_time'), _t('detail.photo_camera_make'), _t('detail.photo_camera_model'), _t('detail.photo_lens'), _t('detail.photo_aperture'), _t('detail.photo_max_aperture'), _t('detail.photo_exposure'), _t('detail.photo_exposure_comp'), _t('detail.photo_iso'), _t('detail.photo_focal'), _t('detail.photo_metering'), _t('detail.photo_flash'), _t('detail.photo_white_balance'), _t('detail.photo_exposure_program')]) {
                if (raw[key]) addRow(key, raw[key]);
            }
        }

        // AI 生成参数
        if (hasParams) {
            newSection();
            for (const [k, v] of Object.entries(params)) {
                addRow(k, v);
            }
        }

        if (!hasExif && !hasParams && !hasPrompt) {
            const empty = document.createElement('div');
            empty.className = 'iv-params-empty';
            empty.textContent = _t('detail.no_param_info');
            body.appendChild(empty);
        }
    }

    function applyOriginalSize() {
        if (!naturalWidth || !naturalHeight) return;
        scale = 1;
        offsetX = 0;
        offsetY = 0;
        applyTransform();
    }

    function resetTransform() {
        showOriginalSize = false;
        const btn = document.getElementById('shortcutOriginal');
        if (btn) btn.classList.remove('active');
        fitToContainer();
    }

    function applyTransform() {
        img.style.transform = `translate(${offsetX}px, ${offsetY}px) scale(${scale}) rotate(${rotation}deg)`;
        zoomLevelEl.textContent = Math.round(scale * 100) + '%';
    }

    // 初始化
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

    return {
        init,
        open,
        close,
        rotate,
        resetTransform
    };
})();
