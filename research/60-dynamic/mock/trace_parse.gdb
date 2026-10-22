set pagination off
break *0x42cad8
commands
silent
printf "HIT TLV\n"
continue
end
break *0x413c70
commands
silent
printf "HIT AUTHRESULT\n"
continue
end
break *0x44f8e6
commands
silent
printf "HIT MAP\n"
continue
end
run -c /tmp/ajs-lab/ajs.conf connect Test*
