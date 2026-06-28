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
})();
