(function(){
  var root=document.documentElement;
  var saved=localStorage.getItem("drang-theme");
  if(saved){ root.setAttribute("data-theme",saved); }
  var btn=document.getElementById("theme");
  if(btn){ btn.addEventListener("click",function(){
    var cur=root.getAttribute("data-theme");
    var dark = cur ? cur==="dark" : matchMedia("(prefers-color-scheme: dark)").matches;
    var next = dark ? "light" : "dark";
    root.setAttribute("data-theme",next);
    localStorage.setItem("drang-theme",next);
  }); }
  document.querySelectorAll(".copy").forEach(function(b){
    b.addEventListener("click",function(){
      navigator.clipboard.writeText(b.getAttribute("data-copy"));
      var t=b.textContent; b.textContent="copied"; setTimeout(function(){b.textContent=t;},1200);
    });
  });

  // code-example carousel
  var demo=document.getElementById("demo");
  if(demo){
    var slides=Array.prototype.slice.call(demo.querySelectorAll(".slide"));
    if(slides.length>1){
      var view=demo.querySelector(".demo-view");
      var label=document.getElementById("demoLabel");
      var dotsWrap=document.getElementById("demoDots");
      var reduce=matchMedia("(prefers-reduced-motion: reduce)").matches;
      var idx=0, timer=null;

      var dots=slides.map(function(s,i){
        var d=document.createElement("button");
        d.className="dot"; d.type="button"; d.setAttribute("role","tab");
        d.setAttribute("aria-label",s.getAttribute("data-label")||("Example "+(i+1)));
        d.addEventListener("click",function(){ go(i); kick(); });
        dotsWrap.appendChild(d);
        return d;
      });

      function fit(){   // size the viewport to the tallest slide so switching never jumps
        var max=0;
        slides.forEach(function(s){ var h=s.hidden; s.hidden=false;
          max=Math.max(max,s.offsetHeight); s.hidden=h; });
        view.style.minHeight=max+"px";
      }
      function go(i){
        idx=(i+slides.length)%slides.length;
        slides.forEach(function(s,j){ s.hidden=j!==idx; });
        dots.forEach(function(d,j){ d.setAttribute("aria-selected",j===idx?"true":"false"); });
        if(label) label.textContent=slides[idx].getAttribute("data-label")||"";
        if(!reduce){ var s=slides[idx]; s.style.animation="none"; void s.offsetWidth; s.style.animation=""; }
      }
      function kick(){   // (re)start auto-advance
        if(timer){ clearInterval(timer); timer=null; }
        if(!reduce) timer=setInterval(function(){ go(idx+1); },6000);
      }
      function stop(){ if(timer){ clearInterval(timer); timer=null; } }

      var nb=document.getElementById("demoNext"), pb=document.getElementById("demoPrev");
      if(nb) nb.addEventListener("click",function(){ go(idx+1); kick(); });
      if(pb) pb.addEventListener("click",function(){ go(idx-1); kick(); });
      demo.addEventListener("mouseenter",stop);
      demo.addEventListener("mouseleave",kick);
      demo.addEventListener("focusin",stop);
      demo.addEventListener("focusout",kick);
      demo.addEventListener("keydown",function(e){
        if(e.key==="ArrowRight"){ go(idx+1); kick(); }
        else if(e.key==="ArrowLeft"){ go(idx-1); kick(); }
      });

      var rt; window.addEventListener("resize",function(){ clearTimeout(rt); rt=setTimeout(fit,150); });
      fit(); go(0); kick();
    }
  }
})();
