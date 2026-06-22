// Blog — 前端增强脚本
(function(){
'use strict';

/* ══════════════════════════════════════════════════════════════
   1. 深色模式
   ══════════════════════════════════════════════════════════════ */
(function(){
  var key = 'blog-theme';
  var saved = localStorage.getItem(key);
  var preferDark = window.matchMedia('(prefers-color-scheme: dark)').matches;

  function apply(theme){
    document.documentElement.setAttribute('data-theme', theme);
    localStorage.setItem(key, theme);
  }

  // 初始化
  if(saved === 'dark' || (!saved && preferDark)){
    apply('dark');
  } else {
    apply(saved || 'light');
  }

  // 监听系统切换
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function(e){
    if(!localStorage.getItem(key)){
      apply(e.matches ? 'dark' : 'light');
    }
  });

  // 暴露切换函数
  window.toggleTheme = function(){
    var cur = document.documentElement.getAttribute('data-theme');
    apply(cur === 'dark' ? 'light' : 'dark');
  };
})();

/* ══════════════════════════════════════════════════════════════
   2. 移动端导航抽屉
   ══════════════════════════════════════════════════════════════ */
(function(){
  var btn = document.getElementById('navToggle');
  var nav = document.querySelector('.top-nav');
  if(!btn || !nav) return;

  btn.addEventListener('click', function(){
    nav.classList.toggle('nav-open');
    btn.setAttribute('aria-expanded', nav.classList.contains('nav-open'));
  });

  // 点击 nav 链接自动关闭
  nav.querySelectorAll('.nav-links a, .nav-auth a').forEach(function(a){
    a.addEventListener('click', function(){ nav.classList.remove('nav-open'); });
  });
})();

/* ══════════════════════════════════════════════════════════════
   3. 阅读时间估算
   ══════════════════════════════════════════════════════════════ */
(function(){
  var el = document.getElementById('readingTime');
  if(!el) return;
  var content = document.querySelector('.post-content');
  if(!content) return;
  var text = content.textContent || '';
  var cjk = (text.match(/[\u4e00-\u9fff\u3400-\u4dbf\uf900-\ufaff]/g) || []).length;
  var words = text.replace(/[\u4e00-\u9fff\u3400-\u4dbf\uf900-\ufaff]/g, ' ').split(/\s+/).filter(Boolean).length;
  var minutes = Math.max(1, Math.round((cjk / 300 + words / 200)));
  el.textContent = minutes + ' 分钟';
})();

/* ══════════════════════════════════════════════════════════════
   4. 分享按钮
   ══════════════════════════════════════════════════════════════ */
(function(){
  var btn = document.getElementById('shareBtn');
  if(!btn) return;
  btn.addEventListener('click', function(){
    var url = window.location.href;
    var title = document.title;
    if(navigator.share){
      navigator.share({title: title, url: url}).catch(function(){});
    } else {
      navigator.clipboard.writeText(url).then(function(){
        var orig = btn.textContent;
        btn.textContent = '✅ 已复制';
        setTimeout(function(){ btn.textContent = orig; }, 2000);
      }).catch(function(){});
    }
  });
})();

/* ══════════════════════════════════════════════════════════════
   5. 图片灯箱
   ══════════════════════════════════════════════════════════════ */
(function(){
  var imgs = document.querySelectorAll('.post-content img, .post-detail-cover');
  if(!imgs.length) return;

  var overlay = document.createElement('div');
  overlay.className = 'lightbox';
  overlay.innerHTML = '<div class="lightbox-bg"></div><img class="lightbox-img" alt=""><button class="lightbox-close">&times;</button>';
  document.body.appendChild(overlay);

  var lbImg = overlay.querySelector('.lightbox-img');
  var lbClose = overlay.querySelector('.lightbox-close');

  function open(src){
    lbImg.src = src;
    overlay.classList.add('active');
    document.body.style.overflow = 'hidden';
  }
  function close(){
    overlay.classList.remove('active');
    document.body.style.overflow = '';
  }

  [].forEach.call(imgs, function(img){
    img.style.cursor = 'zoom-in';
    img.addEventListener('click', function(){ open(img.src); });
  });

  lbClose.addEventListener('click', close);
  overlay.addEventListener('click', function(e){ if(e.target === overlay || e.target.classList.contains('lightbox-bg')) close(); });
  document.addEventListener('keydown', function(e){ if(e.key === 'Escape') close(); });
})();

/* ══════════════════════════════════════════════════════════════
   6. 文章点赞（前端交互）
   ══════════════════════════════════════════════════════════════ */
(function(){
  var btn = document.getElementById('likeBtn');
  if(!btn) return;

  btn.addEventListener('click', function(){
    var postId = btn.getAttribute('data-post-id');
    if(!postId) return;

    btn.classList.add('loading');

    fetch('/post/like', {
      method: 'POST',
      headers: {'Content-Type': 'application/x-www-form-urlencoded'},
      body: 'post_id=' + encodeURIComponent(postId)
    }).then(function(r){ return r.json(); }).then(function(d){
      if(d.ok){
        btn.querySelector('.like-count').textContent = d.count;
        btn.classList.toggle('liked', d.liked);
      }
    }).catch(function(){}).finally(function(){
      btn.classList.remove('loading');
    });
  });
})();

/* ══════════════════════════════════════════════════════════════
   7. 页面过渡动画
   ══════════════════════════════════════════════════════════════ */
(function(){
  document.documentElement.classList.add('js-ready');
})();

/* ══════════════════════════════════════════════════════════════
   8. 深色模式切换按钮（注入到 nav）
   ══════════════════════════════════════════════════════════════ */
(function(){
  var nav = document.querySelector('.top-nav .nav-auth');
  if(!nav) return;
  var btn = document.createElement('button');
  btn.className = 'theme-toggle';
  btn.setAttribute('aria-label', '切换深色/浅色模式');
  btn.innerHTML = '<span class="theme-icon">🌙</span><span class="theme-label"></span>';
  btn.addEventListener('click', function(){
    window.toggleTheme();
    syncThemeUI();
  });

  function syncThemeUI(){
    var theme = document.documentElement.getAttribute('data-theme');
    var icon = btn.querySelector('.theme-icon');
    var label = btn.querySelector('.theme-label');
    icon.textContent = theme === 'dark' ? '☀️' : '🌙';
    // 致敬 Xinghuisama：深色模式赋予诗意命名
    label.textContent = theme === 'dark' ? '流萤深空' : '';
  }

  syncThemeUI();
  // 监听系统主题变化时同步图标
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', syncThemeUI);
  nav.insertBefore(btn, nav.firstChild);
})();

/* ══════════════════════════════════════════════════════════════
   9. Hero 终端启动动画 —— 致敬 Xinghuisama 的 INITIALIZING SYSTEM
   ══════════════════════════════════════════════════════════════ */
(function(){
  var boot = document.getElementById('heroBoot');
  var hero = document.querySelector('.hero');
  if(!boot || !hero) return;

  var lines = [
    '> INITIALIZING SYSTEM',
    '> LOADING ARCHIVE...',
    '> CONNECTING...'
  ];
  var full = lines.join('  ');
  var idx = 0;

  function type(){
    if(idx > full.length){
      boot.innerHTML = full + '<span class="cursor"></span>';
      setTimeout(function(){
        hero.classList.add('ready');
        // 启动文字渐隐，让 motto 成为主角
        setTimeout(function(){ boot.style.transition = 'opacity 0.8s ease'; boot.style.opacity = '0.5'; }, 1200);
      }, 200);
      return;
    }
    boot.innerHTML = full.slice(0, idx) + '<span class="cursor"></span>';
    idx += 1;
    setTimeout(type, 28 + Math.random() * 40);
  }
  // 略微延迟启动，等待字体加载
  setTimeout(type, 200);
})();

/* ══════════════════════════════════════════════════════════════
   10. 阅读进度条
   ══════════════════════════════════════════════════════════════ */
(function(){
  var bar = document.querySelector('.reading-progress');
  if(!bar) return;
  var target = document.querySelector('.post-detail') || document.querySelector('.post-content');
  if(!target) return;

  function update(){
    var rect = target.getBoundingClientRect();
    var total = rect.height;
    var passed = Math.min(Math.max(-rect.top + 100, 0), total);
    var pct = total > 0 ? (passed / total) * 100 : 0;
    bar.style.width = pct + '%';
  }
  window.addEventListener('scroll', update, { passive: true });
  window.addEventListener('resize', update);
  update();
})();

/* ══════════════════════════════════════════════════════════════
   11. 代码块增强（语言标签 + 复制按钮）
   ══════════════════════════════════════════════════════════════ */
(function(){
  var pres = document.querySelectorAll('.post-content pre');
  if(!pres.length) return;

  [].forEach.call(pres, function(pre){
    var code = pre.querySelector('code');
    if(!code) return;

    // 推断语言：class="language-xxx"
    var lang = '';
    var m = /language-([\w-]+)/.exec(code.className);
    if(m) lang = m[1];

    if(lang){
      var tag = document.createElement('span');
      tag.className = 'code-lang';
      tag.textContent = lang;
      pre.appendChild(tag);
    }

    var copy = document.createElement('button');
    copy.className = 'code-copy';
    copy.type = 'button';
    copy.textContent = '复制';
    copy.addEventListener('click', function(){
      var text = code.textContent;
      if(navigator.clipboard){
        navigator.clipboard.writeText(text).then(done, done);
      } else {
        var ta = document.createElement('textarea');
        ta.value = text; document.body.appendChild(ta); ta.select();
        try { document.execCommand('copy'); } catch(e) {}
        document.body.removeChild(ta);
        done();
      }
      function done(){
        copy.textContent = '已复制';
        copy.classList.add('copied');
        setTimeout(function(){ copy.textContent = '复制'; copy.classList.remove('copied'); }, 1600);
      }
    });
    pre.appendChild(copy);
  });
})();

/* ══════════════════════════════════════════════════════════════
   12. 返回顶部按钮
   ══════════════════════════════════════════════════════════════ */
(function(){
  var btn = document.querySelector('.back-to-top');
  if(!btn) return;

  function onScroll(){
    if(window.pageYOffset > 400){
      btn.classList.add('visible');
    } else {
      btn.classList.remove('visible');
    }
  }
  btn.addEventListener('click', function(){
    window.scrollTo({ top: 0, behavior: 'smooth' });
  });
  window.addEventListener('scroll', onScroll, { passive: true });
  onScroll();
})();

/* ══════════════════════════════════════════════════════════════
   13. 深色模式流萤粒子背景 —— 致敬 Xinghuisama 的“流萤飞舞的深空”
   ══════════════════════════════════════════════════════════════ */
(function(){
  var canvas = document.querySelector('.firefly-canvas');
  if(!canvas) return;
  // 移动端性能考虑：仅在较大屏幕开启
  if(window.matchMedia('(max-width: 720px)').matches) return;
  if(window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

  var ctx = canvas.getContext('2d');
  var fireflies = [];
  var COUNT = 36;
  var raf = null;
  var running = false;

  function resize(){
    canvas.width = window.innerWidth;
    canvas.height = window.innerHeight;
  }

  function spawn(){
    fireflies = [];
    for(var i = 0; i < COUNT; i++){
      fireflies.push({
        x: Math.random() * canvas.width,
        y: Math.random() * canvas.height,
        r: Math.random() * 1.6 + 0.6,
        vx: (Math.random() - 0.5) * 0.25,
        vy: (Math.random() - 0.5) * 0.25,
        phase: Math.random() * Math.PI * 2,
        speed: 0.01 + Math.random() * 0.02,
        hue: Math.random() < 0.5 ? 160 : 38
      });
    }
  }

  function draw(){
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    for(var i = 0; i < fireflies.length; i++){
      var f = fireflies[i];
      f.x += f.vx; f.y += f.vy; f.phase += f.speed;
      if(f.x < -10) f.x = canvas.width + 10;
      if(f.x > canvas.width + 10) f.x = -10;
      if(f.y < -10) f.y = canvas.height + 10;
      if(f.y > canvas.height + 10) f.y = -10;
      var alpha = 0.25 + (Math.sin(f.phase) + 1) * 0.3;
      var glow = ctx.createRadialGradient(f.x, f.y, 0, f.x, f.y, f.r * 6);
      glow.addColorStop(0, 'hsla(' + f.hue + ', 90%, 70%, ' + alpha + ')');
      glow.addColorStop(1, 'hsla(' + f.hue + ', 90%, 70%, 0)');
      ctx.fillStyle = glow;
      ctx.beginPath();
      ctx.arc(f.x, f.y, f.r * 6, 0, Math.PI * 2);
      ctx.fill();
      ctx.fillStyle = 'hsla(' + f.hue + ', 100%, 85%, ' + Math.min(alpha + 0.2, 1) + ')';
      ctx.beginPath();
      ctx.arc(f.x, f.y, f.r, 0, Math.PI * 2);
      ctx.fill();
    }
    raf = requestAnimationFrame(draw);
  }

  function start(){
    if(running) return;
    running = true;
    resize(); spawn(); draw();
  }
  function stop(){
    if(!running) return;
    running = false;
    if(raf) cancelAnimationFrame(raf);
    ctx.clearRect(0, 0, canvas.width, canvas.height);
  }

  function sync(){
    if(document.documentElement.getAttribute('data-theme') === 'dark'){
      start();
    } else {
      stop();
    }
  }

  window.addEventListener('resize', function(){ if(running){ resize(); spawn(); } });
  // 初次同步 + 监听切换
  sync();
  // theme toggle 改变 data-theme 后同步
  var observer = new MutationObserver(sync);
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });

  // 节能：页面不可见时暂停动画，可见时恢复，避免后台标签页耗电。
  document.addEventListener('visibilitychange', function(){
    if(document.hidden){
      stop();
    } else {
      sync();
    }
  });
})();

/* ══════════════════════════════════════════════════════════════
   14. 卡片错落入场动画索引
   ══════════════════════════════════════════════════════════════ */
(function(){
  var cards = document.querySelectorAll('.post-card');
  [].forEach.call(cards, function(card, i){
    card.style.setProperty('--i', i);
  });
})();

/* ══════════════════════════════════════════════════════════════
   15. 文章页 TOC 目录生成 + 滚动高亮
   ══════════════════════════════════════════════════════════════ */
(function(){
  var toc = document.getElementById('postToc');
  var content = document.querySelector('.post-content');
  if(!toc || !content) return;

  var heads = content.querySelectorAll('h2, h3');
  if(heads.length < 3) return; // 标题太少不显示目录

  var html = '<div class="post-toc-title">On This Page</div><ul>';
  var links = [];
  [].forEach.call(heads, function(h, i){
    if(!h.id){
      h.id = 'toc-h-' + i;
    }
    var cls = h.tagName === 'H3' ? 'toc-h3' : '';
    html += '<li><a class="' + cls + '" href="#' + h.id + '" data-target="' + h.id + '">' + escapeHTML(h.textContent) + '</a></li>';
    links.push({id: h.id, el: h, link: null});
  });
  html += '</ul>';
  toc.innerHTML = html;

  toc.classList.add('visible');

  // 绑定 link 引用
  links.forEach(function(l){
    l.link = toc.querySelector('a[data-target="' + l.id + '"]');
  });

  // 平滑滚动 + 锚点点击
  toc.addEventListener('click', function(e){
    var a = e.target.closest('a[data-target]');
    if(!a) return;
    e.preventDefault();
    var target = document.getElementById(a.getAttribute('data-target'));
    if(target){
      var top = target.getBoundingClientRect().top + window.pageYOffset - 90;
      window.scrollTo({ top: top, behavior: 'smooth' });
    }
  });

  // 滚动高亮当前章节
  function updateActive(){
    var scrollY = window.pageYOffset + 120;
    var current = null;
    for(var i = 0; i < links.length; i++){
      if(links[i].el.getBoundingClientRect().top + window.pageYOffset <= scrollY){
        current = links[i];
      } else {
        break;
      }
    }
    links.forEach(function(l){ if(l.link) l.link.classList.remove('active'); });
    if(current && current.link) current.link.classList.add('active');
  }
  window.addEventListener('scroll', updateActive, { passive: true });
  updateActive();

  function escapeHTML(s){
    return String(s).replace(/[&<>"']/g, function(c){
      return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c];
    });
  }
})();

/* ══════════════════════════════════════════════════════════════
   16. 文章图片懒加载 —— 减少首屏请求，降低 CLS
   ══════════════════════════════════════════════════════════════ */
(function(){
  // 为文章内容中的图片统一添加 loading="lazy" 与 decoding="async"，
  // 浏览器原生懒加载，无需 JS 监听滚动，性能更优。
  var imgs = document.querySelectorAll('.post-content img, .post-detail-cover');
  if(!imgs.length) return;
  [].forEach.call(imgs, function(img){
    if(!img.hasAttribute('loading')) img.setAttribute('loading', 'lazy');
    if(!img.hasAttribute('decoding')) img.setAttribute('decoding', 'async');
  });
})();

})();
